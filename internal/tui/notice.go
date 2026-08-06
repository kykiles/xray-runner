package tui

import (
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Уведомление, которое само уходит с экрана. Терминал не умеет прозрачности,
// поэтому «плавно исчезнуть» — это шагами приблизить цвет текста к фону.

// noticeFade is the ramp a finished message walks down on its way out. It ends
// well below the dimmest thing on screen rather than at the background colour
// itself: the background is only black if the user's terminal says so. On a
// console without truecolor the steps collapse into one and the message simply
// disappears — the same story told shorter.
var noticeFade = []string{colorCoffee, colorSocket, "#5a5a5a", "#3a3a3a"}

const (
	noticeHold = 2 * time.Second        // read it before it starts to go
	noticeStep = 150 * time.Millisecond // one rung of the ramp
)

// noticeTickMsg advances a fade. gen tags the notice it belongs to, so the
// ticks of a message that has already been replaced do not blank its successor.
type noticeTickMsg struct{ gen int }

// notice is the one-line message under a screen's list. Every message fades out
// on its own, failures included: the screen stays minimal and the detail worth
// keeping goes to the log (failErr), not onto a line left lit forever.
type notice struct {
	text  string
	style lipgloss.Style
	step  int // 0 while fully lit, then one per rung of noticeFade
	gen   int
}

// ok/fail/warn show a message and start it fading. The returned command must be
// handed back to bubbletea, or the message stays lit forever.
func (n *notice) ok(text string) tea.Cmd   { return n.set(text, okStyle) }
func (n *notice) fail(text string) tea.Cmd { return n.set(text, errStyle) }
func (n *notice) warn(text string) tea.Cmd { return n.set(text, warnStyle) }

// failErr is the pair the screens use for anything that went wrong: a short line
// the user reads, the whole error in the log. A raw error on screen ran to the
// full URL of a failed fetch and pushed the layout around.
func (n *notice) failErr(text string, err error) tea.Cmd {
	slog.Error(text, "error", err)
	return n.fail(text)
}

// clear takes the line back immediately.
func (n *notice) clear() { n.set("", okStyle) }

func (n *notice) set(text string, style lipgloss.Style) tea.Cmd {
	n.gen++
	n.text, n.style, n.step = text, style, 0
	if text == "" {
		return nil
	}
	return n.wait(noticeHold)
}

func (n notice) wait(d time.Duration) tea.Cmd {
	gen := n.gen
	return tea.Tick(d, func(time.Time) tea.Msg { return noticeTickMsg{gen: gen} })
}

// tick walks one rung down the ramp, and blanks the line off its end.
func (n *notice) tick(msg noticeTickMsg) tea.Cmd {
	if msg.gen != n.gen || n.text == "" {
		return nil
	}
	n.step++
	if n.step > len(noticeFade) {
		n.text = ""
		return nil
	}
	return n.wait(noticeStep)
}

func (n notice) empty() bool { return n.text == "" }

// view renders the line at its current brightness.
func (n notice) view() string {
	switch {
	case n.text == "":
		return ""
	case n.step == 0:
		return n.style.Render(n.text)
	case n.step <= len(noticeFade):
		return lipgloss.NewStyle().Foreground(lipgloss.Color(noticeFade[n.step-1])).Render(n.text)
	}
	return ""
}
