package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// connectDoneMsg carries the bring-up result back into the connecting model.
type connectDoneMsg struct{ err error }
type connectTickMsg struct{}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type connectingModel struct {
	title   string
	connect func() error
	frame   int
	err     error
}

// ShowConnecting keeps the alt-screen up while connect runs — starting xray and
// bringing proxy/TUN up — so the shell never flashes between the server list and
// the status screen (task #2). It returns connect's error. Keys are ignored:
// bring-up is a short, uninterruptible step, matching the previous synchronous
// behaviour.
func ShowConnecting(title string, connect func() error) error {
	m := connectingModel{title: title, connect: connect}
	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return res.(connectingModel).err
}

func (m connectingModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return connectDoneMsg{err: m.connect()} },
		connectTick(),
	)
}

func connectTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return connectTickMsg{} })
}

func (m connectingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case connectDoneMsg:
		m.err = msg.err
		return m, tea.Quit
	case connectTickMsg:
		m.frame++
		return m, connectTick()
	}
	return m, nil
}

func (m connectingModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Подключение") + "\n\n")
	spinner := cursorStyle.Render(spinnerFrames[m.frame%len(spinnerFrames)])
	b.WriteString("  " + spinner + " Подключение к «" + m.title + "»…\n")
	b.WriteString(legend("  запуск ядра и настройка маршрутизации"))
	return b.String()
}
