package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// connectDoneMsg carries the bring-up result back into the connecting model.
type connectDoneMsg struct{ err error }

// ErrConnectCanceled reports that the user interrupted the bring-up. The caller
// must still run its teardown: the core may be running and routes may be half
// applied by the time the canceled bring-up returns.
var ErrConnectCanceled = errors.New("подключение отменено")

type connectingModel struct {
	title     string
	connect   func() error
	cancel    func()
	canceling bool
	spinner   spinner.Model
	err       error
}

// ShowConnecting keeps the alt-screen up while connect runs — starting xray and
// bringing proxy/TUN up — so the shell never flashes between the server list and
// the status screen (task #2). It returns connect's error.
//
// Ctrl+C calls cancel and then waits for connect to return (M-2). Waiting is the
// point: bring-up edits the routing table, so closing the screen the moment the
// key arrives would let the caller's teardown race a bring-up still in flight.
// A canceled bring-up reports ErrConnectCanceled whatever connect itself
// returned — a Ctrl+C landing exactly as connect succeeded still means "cancel",
// and the caller must tear the session down.
func ShowConnecting(title string, connect func() error, cancel func()) error {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(cursorStyle))
	m := connectingModel{title: title, connect: connect, cancel: cancel, spinner: sp}
	res, err := runScreen(m)
	if err != nil {
		return err
	}
	return res.(connectingModel).err
}

func (m connectingModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return connectDoneMsg{err: m.connect()} },
		m.spinner.Tick,
	)
}

func (m connectingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case connectDoneMsg:
		m.err = msg.err
		if m.canceling {
			m.err = ErrConnectCanceled
		}
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		// U-1: Ctrl+C always quits — here by cancelling and waiting, not by
		// closing the screen out from under an in-flight bring-up.
		if msg.Type == tea.KeyCtrlC && !m.canceling {
			m.canceling = true
			if m.cancel != nil {
				m.cancel()
			}
		}
		return m, nil
	}
	return m, nil
}

func (m connectingModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  Подключение") + "\n\n")
	if m.canceling {
		b.WriteString("  " + m.spinner.View() + " " + warnStyle.Render("Отмена, откатываю настройки…") + "\n")
		b.WriteString(legend(0, "  подождите…"))
		return b.String()
	}
	b.WriteString("  " + m.spinner.View() + " " + textStyle.Render("Подключение к «"+m.title+"»…") + "\n")
	b.WriteString(legend(0, "  запуск ядра и настройка маршрутизации · ctrl+c отменить"))
	return b.String()
}
