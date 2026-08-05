package tui

import (
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

// notice is the one-line message under a screen's list. Success fades out on
// its own; failures stay, because losing the reason a subscription would not
// load after three seconds is worse than a line left on screen.
type notice struct {
	text  string
	style lipgloss.Style
	fades bool
	step  int // 0 while fully lit, then one per rung of noticeFade
	gen   int
}

// ok shows a success and starts it fading. The returned command must be handed
// back to bubbletea, or the message stays lit forever.
func (n *notice) ok(text string) tea.Cmd { return n.set(text, okStyle, true) }

// fail and warn stay until something replaces them or the user moves on.
func (n *notice) fail(text string) { n.set(text, errStyle, false) }
func (n *notice) warn(text string) { n.set(text, warnStyle, false) }

// clear takes the line back immediately.
func (n *notice) clear() { n.set("", okStyle, false) }

func (n *notice) set(text string, style lipgloss.Style, fades bool) tea.Cmd {
	n.gen++
	n.text, n.style, n.fades, n.step = text, style, fades, 0
	if !fades || text == "" {
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
	if msg.gen != n.gen || !n.fades || n.text == "" {
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
