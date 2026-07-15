// Package tui contains the interactive bubbletea screens: subscription list
// and server list (WS-8). Non-TTY runs bypass it via --server/--last (U-2).
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	// urlStyle renders a revealed subscription URL: underlined and unfaded so the
	// terminal shows it as a link and the whole token can be copied.
	urlStyle = lipgloss.NewStyle().Underline(true)
	// legendStyle renders the uniform key legend at the bottom of every
	// screen (U-1).
	legendStyle = lipgloss.NewStyle().Faint(true).MarginTop(1)
)

func legend(items string) string {
	return legendStyle.Render(items)
}

// pad right-pads s to w display columns. Server names carry flag emoji, which
// occupy two columns each, so %-*s (byte-based) would misalign the table.
func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// truncate shortens s to w display columns, keeping the table from wrapping on
// narrow terminals.
func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if lipgloss.Width(b.String()+string(r)) > w-1 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// header renders a table header row for the list screens.
func header(cols string) string {
	return dimStyle.Render(cols) + "\n"
}
