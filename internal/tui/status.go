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
	Protocol string // "vless / tcp / reality" or "balancer/leastLoad · 33 сервера"
	Mode     string // "PROXY (127.0.0.1:10809)"
	NextMode string // mode offered by the m key, e.g. "TUN"
	// Apps is the split-tunnel line: which of the listed processes were actually
	// captured. Empty when apps.txt is empty; the "nothing running" case arrives
	// as a note instead, because it needs saying only once.
	Apps string
}

// StatusUpdate is one health-check result pushed by the app.
type StatusUpdate struct {
	OK      bool
	Latency time.Duration
	Note    string // transient message, e.g. why a mode switch was refused
	Err     bool   // render Note as an error
}

type statusModel struct {
	info    StatusInfo
	updates <-chan StatusUpdate

	started time.Time
	last    *StatusUpdate
	note    string
	noteErr bool
	action  StatusAction
	width   int // terminal width; 0 until the first WindowSizeMsg
}

type statusTickMsg time.Time

// ShowStatus renders the connected screen until the user picks an action.
// updates streams health results; it is closed by the caller when the session
// ends on its own (e.g. the core died), which closes the screen with StatusQuit.
func ShowStatus(info StatusInfo, updates <-chan StatusUpdate) (StatusAction, error) {
	m := statusModel{
		info:    info,
		updates: updates,
		started: time.Now(),
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
		return m, nil
	case statusTickMsg:
		return m, statusTick()
	case sessionEndedMsg:
		m.action = StatusQuit
		return m, tea.Quit
	case StatusUpdate:
		if msg.Note != "" {
			m.note, m.noteErr = msg.Note, msg.Err
		} else {
			u := msg
			m.last = &u
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
		}
	}
	return m, nil
}

func (m statusModel) View() string {
	var b strings.Builder
	// The title carries the same two-column indent as the rows below it, so the
	// whole screen lines up on one left edge (task #3).
	b.WriteString(titleStyle.Render("  Подключено") + "\n\n")

	row := func(label, value string) {
		b.WriteString("  " + dimStyle.Render(pad(label, 10)) + textStyle.Render(value) + "\n")
	}

	row("Сервер", m.info.Title)
	if m.info.Endpoint != "" {
		row("Адрес", m.info.Endpoint)
	}
	row("Протокол", m.info.Protocol)
	row("Режим", m.info.Mode)
	if m.info.Apps != "" {
		row("Процессы", m.info.Apps)
	}
	// The status row carries its own health colors, so it is written raw.
	b.WriteString("  " + dimStyle.Render(pad("Статус", 10)) + m.health() + "\n")

	if m.note != "" {
		style := warnStyle
		if m.noteErr {
			style = errStyle
		}
		b.WriteString("\n  " + style.Render(m.note) + "\n")
	}

	keys := fmt.Sprintf("  ← назад к серверам · m режим %s · r перезапуск · q выход", m.info.NextMode)
	b.WriteString(legend(m.width, keys))
	return b.String()
}

func (m statusModel) health() string {
	uptime := time.Since(m.started).Round(time.Second)
	if m.last == nil {
		return dimStyle.Render("● проверка… ") + fmt.Sprintf("· uptime %s", fmtDuration(uptime))
	}
	mark := okStyle.Render("● ок")
	if !m.last.OK {
		mark = errStyle.Render("● нет связи")
	}
	out := mark
	if m.last.Latency > 0 {
		out += fmt.Sprintf(" · %d ms", m.last.Latency.Milliseconds())
	}
	return out + fmt.Sprintf(" · uptime %s", fmtDuration(uptime))
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
