// Package tui contains the interactive bubbletea screens: subscription list
// and server list (WS-8). Non-TTY runs bypass it via --server/--last (U-2).
package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	// legendStyle renders the uniform key legend at the bottom of every
	// screen (U-1).
	legendStyle = lipgloss.NewStyle().Faint(true).MarginTop(1)
)

func legend(items string) string {
	return legendStyle.Render(items)
}
