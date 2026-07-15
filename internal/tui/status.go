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

	started  time.Time
	last     *StatusUpdate
	note     string
	noteErr  bool
	action   StatusAction
	quitting bool
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
	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
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
		switch msg.String() {
		case "q":
			m.action = StatusQuit
			return m, tea.Quit
		case "esc", "left":
			m.action = StatusBack
			return m, tea.Quit
		case "m":
			m.action = StatusSwitchMode
			return m, tea.Quit
		case "r":
			m.action = StatusRestart
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m statusModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("── Подключено") + "\n\n")

	row := func(label, value string) {
		b.WriteString("  " + dimStyle.Render(pad(label, 10)) + value + "\n")
	}

	row("Сервер", m.info.Title)
	if m.info.Endpoint != "" {
		row("Адрес", m.info.Endpoint)
	}
	row("Протокол", m.info.Protocol)
	row("Режим", m.info.Mode)
	row("Статус", m.health())

	if m.note != "" {
		style := warnStyle
		if m.noteErr {
			style = errStyle
		}
		b.WriteString("\n  " + style.Render(m.note) + "\n")
	}

	keys := fmt.Sprintf("  esc назад к серверам · m режим %s · r перезапуск · q выход", m.info.NextMode)
	b.WriteString(legend(keys))
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
	h := int(d.Hours())
	mnt := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, mnt, s)
}
