// Package tui contains the interactive bubbletea screens: subscription list
// and server list (WS-8). Non-TTY runs bypass it via --server/--last (U-2).
package tui

import (
	"os"
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

// InitStyles overrides the default palette from the environment so colors can be
// tuned in .env without recompiling. It runs after config.Load has populated the
// environment; env values are ANSI codes (0-255) or lipgloss color names.
func InitStyles() {
	titleStyle = titleStyle.Foreground(envColor("COLOR_TITLE", "6"))
	cursorStyle = cursorStyle.Foreground(envColor("COLOR_CURSOR", "6"))
	errStyle = errStyle.Foreground(envColor("COLOR_ERR", "1"))
	okStyle = okStyle.Foreground(envColor("COLOR_OK", "2"))
	warnStyle = warnStyle.Foreground(envColor("COLOR_WARN", "3"))
	selectedStyle = selectedStyle.Foreground(envColor("COLOR_SELECTED", "6"))
}

func envColor(key, def string) lipgloss.Color {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return lipgloss.Color(v)
	}
	return lipgloss.Color(def)
}

// flagSpace normalizes the gap after a leading flag emoji to exactly one space,
// so the two regional-indicator runes and the text that follows neither collide
// nor drift apart in the table.
func flagSpace(s string) string {
	r := []rune(s)
	if len(r) < 2 || !isRegional(r[0]) || !isRegional(r[1]) {
		return s
	}
	rest := strings.TrimLeft(string(r[2:]), " ")
	if rest == "" {
		return string(r[:2])
	}
	return string(r[:2]) + " " + rest
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// clip shortens an already-styled line to w display columns, keeping table rows
// from wrapping or running past the terminal edge. It uses lipgloss so embedded
// ANSI color codes are measured and cut safely. w<=0 (size not yet known) leaves
// the line untouched.
func clip(s string, w int) string {
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// legend renders the bottom key hints one command per line (vertical), splitting
// the " · "-joined string it receives and re-indenting each entry.
func legend(items string) string {
	parts := strings.Split(strings.TrimLeft(items, " "), " · ")
	for i := range parts {
		parts[i] = "  " + parts[i]
	}
	return legendStyle.Render(strings.Join(parts, "\n"))
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
