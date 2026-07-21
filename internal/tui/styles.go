// Package tui contains the interactive bubbletea screens: subscription list
// and server list (WS-8). Non-TTY runs bypass it via --server/--last (U-2).
package tui

import (
	"fmt"
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

// legend renders the bottom key hints on one line, indented to the same left
// edge as the rest of the screen. A terminal too narrow for the whole line falls
// back to one command per line, so no hint gets cut off. width<=0 (size not yet
// known) assumes the line fits.
func legend(width int, items string) string {
	line := strings.TrimLeft(items, " ")
	if width <= 0 || lipgloss.Width(line)+2 <= width {
		return legendStyle.Render("  " + line)
	}
	parts := strings.Split(line, " · ")
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

// window returns the [start,end) sub-range of a total-row list to render so that
// cursor stays visible within height rows, plus how many rows fall above and
// below the window (task #3). A non-positive height (size unknown) or a list
// that already fits shows everything.
func window(total, cursor, height int) (start, end, above, below int) {
	if height <= 0 || total <= height {
		return 0, total, 0, 0
	}
	start = cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > total {
		start = total - height
	}
	end = start + height
	return start, end, start, total - end
}

// legendHeight is how many terminal lines legend(width, keys) occupies, counted
// off the rendered result so the two never drift apart. List screens reserve it
// so the row window never pushes the legend off the bottom.
func legendHeight(width int, keys string) int {
	return strings.Count(legend(width, keys), "\n") + 1
}

// moreUp/moreDown render the scroll indicators for a windowed list. Both always
// occupy a line — an empty one when nothing is hidden — so the rows below the
// header keep their place instead of jumping by a line as the window scrolls
// past either end of the list.
func moreUp(n int) string {
	if n <= 0 {
		return "\n"
	}
	return "  " + dimStyle.Render(fmt.Sprintf("── ещё %d ↑ ──", n)) + "\n"
}

func moreDown(n int) string {
	if n <= 0 {
		return "\n"
	}
	return "  " + dimStyle.Render(fmt.Sprintf("── ещё %d ↓ ──", n)) + "\n"
}
