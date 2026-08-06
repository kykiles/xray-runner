package tui

import (
	"bytes"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Every screen is its own bubbletea program, and each one gives the terminal
// back to the shell when it ends. Between two screens the shell shows through —
// plainly visible while a session tears down (stopping the core, undoing the
// proxy) before the server list comes back. Keeping the alternate buffer up in
// the gap removes the flash; the app hands the terminal back on exit.
const (
	altEnter = "\x1b[?1049h"
	altExit  = "\x1b[?1049l"
)

var held bool

// altKeeper is stdout with the leave-the-alternate-buffer sequence filtered out.
// bubbletea prints it as a screen's program ends, and re-entering afterwards is
// one frame too late: the shell shows through in between, which is the flicker
// on every ←/→. Swallowing it holds the buffer up across the whole menu;
// ReleaseScreen is what gives it back.
type altKeeper struct{ *os.File }

func (a altKeeper) Write(b []byte) (int, error) {
	i := bytes.Index(b, []byte(altExit))
	if i < 0 {
		return a.File.Write(b)
	}
	if _, err := a.File.Write(append(bytes.Clone(b[:i]), b[i+len(altExit):]...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

// onResize is what every screen returns from a tea.WindowSizeMsg. Windows keeps
// the cursor-visibility flag per screen buffer and resets it when the buffer is
// resized, so the hidden cursor comes back blinking at the start of the last
// rendered line (the legend); ClearScreen drops whatever the console reflowed
// while resizing, which is what left a second copy of the legend on screen.
func onResize() tea.Cmd { return tea.Batch(tea.ClearScreen, tea.HideCursor) }

// runScreen runs one full-screen program and keeps the alternate buffer after
// it exits, so nothing flashes before the next screen opens.
func runScreen(m tea.Model) (tea.Model, error) {
	res, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(altKeeper{os.Stdout})).Run()
	held = true
	// Belt and braces: should the sequence ever arrive split across two writes,
	// the filter misses it and this puts the buffer back — a no-op otherwise.
	fmt.Print(altEnter)
	return res, err
}

// ReleaseScreen gives the terminal back to the shell: on exit, and before
// anything the user is meant to read on the normal buffer.
func ReleaseScreen() {
	if !held {
		return
	}
	held = false
	fmt.Print(altExit)
}
