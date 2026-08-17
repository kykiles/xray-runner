package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// StatusAction is what the user asked for while connected.
type StatusAction int

const (
	StatusQuit       StatusAction = iota
	StatusBack                    // disconnect, back to the server list
	StatusSwitchMode              // toggle proxy/tun without leaving the session
	StatusRestart                 // restart the core with the same config
)

// StatusInfo is the static part of the connected screen.
type StatusInfo struct {
	Title    string // server or profile name
	Endpoint string // host:port, empty for a balancer profile
	Protocol string // "vless / tcp / reality" — the pool's stack for a balancer
	// Balancer is the strategy and pool size, empty for a single server. It has
	// its own row because it is not a protocol, and it used to be shown as one.
	Balancer string // "balancer/leastLoad · 33 сервера"
	Mode     string // "PROXY (127.0.0.1:10809)"
	// SplitMode replaces Mode while at least one listed process is captured —
	// that is the only moment traffic is actually routed per process.
	SplitMode string
	NextMode  string // mode offered by the m key, e.g. "TUN"
	// Split says apps.txt has entries, so the process row is drawn even while
	// Apps is empty — the empty case is a live state (nothing listed is running
	// *yet*), not a one-off note that would outlive the truth.
	Split bool
	// Apps is the split-tunnel line: which of the listed processes were actually
	// captured.
	Apps []string
}

// StatusUpdate is one health-check result pushed by the app.
type StatusUpdate struct {
	OK      bool
	Latency time.Duration
	Note    string   // transient message, e.g. why a mode switch was refused
	Err     bool     // render Note as an error
	Apps    []string // split-tunnel rescan result; nil in a plain health update
}

type statusModel struct {
	info    StatusInfo
	updates <-chan StatusUpdate

	started  time.Time
	last     *StatusUpdate
	note     notice
	appsOpen bool // the split-tunnel list is expanded to one process per line
	action   StatusAction
	width    int // terminal width; 0 until the first WindowSizeMsg
}

type statusTickMsg time.Time

// ShowStatus renders the connected screen until the user picks an action.
// updates streams health results; it is closed by the caller when the session
// ends on its own (e.g. the core died), which closes the screen with StatusQuit.
func ShowStatus(info StatusInfo, updates <-chan StatusUpdate) (StatusAction, error) {
	// started stays zero until the first successful health check — see health().
	// A reconnect or a mode switch builds a new screen, so the counter restarts
	// with the connection rather than carrying the old one's age over.
	info.Title = stripEmoji(info.Title)
	m := statusModel{
		info:    info,
		updates: updates,
		action:  StatusQuit,
	}
	res, err := runScreen(m)
	if err != nil {
		return StatusQuit, err
	}
	return res.(statusModel).action, nil
}

func (m statusModel) Init() tea.Cmd {
	return tea.Batch(m.waitUpdate(), statusTick())
}

func (m statusModel) waitUpdate() tea.Cmd {
	ch := m.updates
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return sessionEndedMsg{}
		}
		return u
	}
}

type sessionEndedMsg struct{}

func statusTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return statusTickMsg(t) })
}

func (m statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, onResize()
	case statusTickMsg:
		return m, statusTick()
	case noticeTickMsg:
		return m, m.note.tick(msg)
	case sessionEndedMsg:
		m.action = StatusQuit
		return m, tea.Quit
	case StatusUpdate:
		switch {
		case msg.Apps != nil:
			m.info.Apps = msg.Apps
		case msg.Note != "":
			// The same fading line every other screen uses (notice.go): read once,
			// then out of the way — the detail stays in the log.
			style := warnStyle
			if msg.Err {
				style = errStyle
			}
			return m, tea.Batch(m.note.set(msg.Note, style), m.waitUpdate())
		default:
			u := msg
			m.last = &u
			if u.OK && m.started.IsZero() {
				m.started = time.Now()
			}
		}
		return m, m.waitUpdate()
	case tea.KeyMsg:
		// U-1: Ctrl+C always quits.
		if msg.Type == tea.KeyCtrlC {
			m.action = StatusQuit
			return m, tea.Quit
		}
		// Cyrillic twins mirror the Russian layout (task #7): q→й, m→ь, r→к.
		switch msg.String() {
		case "q", "й":
			m.action = StatusQuit
			return m, tea.Quit
		case "esc", "left":
			m.action = StatusBack
			return m, tea.Quit
		case "m", "ь":
			m.action = StatusSwitchMode
			return m, tea.Quit
		case "r", "к":
			m.action = StatusRestart
			return m, tea.Quit
		case "p", "з":
			m.appsOpen = !m.appsOpen
			return m, nil
		}
	}
	return m, nil
}

// labelWidth is the label column of the connected screen, wide enough for the
// longest label ("Балансировка") plus its gap. The wrapped process list indents
// to the same column, so both read it from here rather than from two numbers
// that drift apart.
const labelWidth = 13

func (m statusModel) View() string {
	var b strings.Builder
	// The title carries the same two-column indent as the rows below it, so the
	// whole screen lines up on one left edge (task #3).
	b.WriteString(titleStyle.Render("  Подключено") + "\n\n")

	row := func(label, value string) {
		b.WriteString("  " + dimStyle.Render(pad(label, labelWidth)) + textStyle.Render(value) + "\n")
	}

	row("Сервер", m.info.Title)
	if m.info.Endpoint != "" {
		row("Адрес", m.info.Endpoint)
	}
	row("Протокол", m.info.Protocol)
	if m.info.Balancer != "" {
		row("Балансировка", m.info.Balancer)
	}
	row("Режим", m.modeLine())
	if m.info.Split {
		row("Процессы", m.appsLine())
	}
	// The status row carries its own health colors, so it is written raw.
	b.WriteString("  " + dimStyle.Render(pad("Статус", labelWidth)) + m.health() + "\n")

	// The notice keeps its two lines, empty or not — same as the list screens:
	// otherwise the legend jumps up the moment a message fades out. It is cut to
	// the terminal width for the same reason: a long message (the tun→proxy
	// fallback runs past 100 columns) wrapped onto a second line on an 80-column
	// console and gave it back when it faded, jumping the legend up a row.
	b.WriteString("\n  " + clip(m.note.view(), m.width-2) + "\n")

	keys := fmt.Sprintf("  ← назад к серверам · m режим %s · r перезапуск · q выход", m.info.NextMode)
	if len(m.info.Apps) > appsPreview {
		keys = fmt.Sprintf("  ← назад · m режим %s · p процессы · r перезапуск · q выход", m.info.NextMode)
	}
	b.WriteString(legend(m.width, keys))
	return b.String()
}

// modeLine names the mode by what is happening right now: SPLIT only while a
// listed process is captured, PROXY the rest of the time. Recomputed on every
// render rather than fixed at connect, so a process that joins mid-session
// renames the row along with the process list below it.
func (m statusModel) modeLine() string {
	if m.info.Split && len(m.info.Apps) > 0 && m.info.SplitMode != "" {
		return m.info.SplitMode
	}
	return m.info.Mode
}

// appsPreview is how many process names the collapsed line shows before it
// gives up and counts the rest: enough to recognise the list at a glance,
// few enough that a screenful of apps cannot push the status row off screen.
const appsPreview = 3

// appsLine renders the split-tunnel processes. Collapsed by default — a long
// list is a wall of names nobody reads — and expanded to one name per line by
// the p key, which is when it is actually being checked.
func (m statusModel) appsLine() string {
	if len(m.info.Apps) == 0 {
		return dimStyle.Render("нет запущенных из apps.txt")
	}
	if m.appsOpen {
		// The extra rows line up under the first name, past the label column.
		return strings.Join(m.info.Apps, "\n  "+strings.Repeat(" ", labelWidth))
	}
	if len(m.info.Apps) <= appsPreview {
		return strings.Join(m.info.Apps, ", ")
	}
	rest := len(m.info.Apps) - appsPreview
	return fmt.Sprintf("%s … +%d (p)", strings.Join(m.info.Apps[:appsPreview], ", "), rest)
}

func (m statusModel) health() string {
	if m.last == nil {
		return dimStyle.Render("● проверка… ")
	}
	mark := okStyle.Render("● ок")
	if !m.last.OK {
		mark = errStyle.Render("● нет связи")
	}
	out := mark
	if m.last.Latency > 0 {
		out += fmt.Sprintf(" · %d ms", m.last.Latency.Milliseconds())
	}
	// No uptime until the connection has actually answered once: the counter
	// measures the live connection, not how long the screen has been open.
	if m.started.IsZero() {
		return out
	}
	return out + fmt.Sprintf(" · uptime %s", fmtDuration(time.Since(m.started).Round(time.Second)))
}

func fmtDuration(d time.Duration) string {
	total := int(d.Seconds())
	days := total / 86400
	h := (total % 86400) / 3600
	mnt := (total % 3600) / 60
	s := total % 60
	if days > 0 {
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, h, mnt, s)
	}
	return fmt.Sprintf("%02d:%02d:%02d", h, mnt, s)
}
