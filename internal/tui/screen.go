package tui

import (
	"fmt"

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

// runScreen runs one full-screen program and keeps the alternate buffer after
// it exits, so nothing flashes before the next screen opens.
func runScreen(m tea.Model) (tea.Model, error) {
	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	held = true
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
