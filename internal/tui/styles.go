// Package tui contains the interactive bubbletea screens: subscription list
// and server list (WS-8). Non-TTY runs bypass it via --server/--last (U-2).
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Grey ramp (docs/colors.jpg): a monochrome scale where prominence is lightness,
// brightest for titles and the selected/best rows, mid-grey for body text, dim
// grey for menu chrome. Hex defaults degrade to 256-color on terminals without
// truecolor; COLOR_* env vars override any of them. Status colors (err/ok/warn)
// stay outside the ramp so failures and successes remain distinguishable, and
// so does the balancer amber: inside the ramp it was one grey among greys, which
// is why the row needed a star beside it to be noticed at all.
const (
	colorSet     = "#f2f2f2" // brightest — the wordmark / titles
	colorCoffee  = "#a5a5a5" // ordinary text — servers, descriptions
	colorGold    = "#d8a657" // balancers — the one accent outside the ramp
	colorSocket  = "#7f7f7f" // menu, legend, cursor chrome
	colorBest    = "#f2f2f2" // fastest ping (bold)
	colorSelBest = "#f2f2f2" // selected row (bold)
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(colorSet))
	// cursorStyle is a menu element (the ▸ marker), so it wears the socket grey.
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSocket)).Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	// textStyle is the ordinary interface text: coffee, per task #6.
	textStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorCoffee))
	// selectedStyle marks the highlighted row — brighter coffee so it stands out
	// against the plain coffee rows without leaving the palette.
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSelBest)).Bold(true)
	// goldStyle marks a balancer row — the only kind that runs as a whole rather
	// than as one server. Colour says what a row is, bold says where the cursor
	// is, so the two never have to compete for the same attribute.
	goldStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorGold))
	// bestStyle marks the fastest measured ping. The list is no longer sorted by
	// latency, so the winner has to stand out where it stands.
	bestStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorBest)).Bold(true)
	// urlStyle renders a revealed subscription URL: underlined and unfaded so the
	// terminal shows it as a link and the whole token can be copied.
	urlStyle = lipgloss.NewStyle().Underline(true)
	// legendStyle renders the uniform key legend at the bottom of every screen
	// (U-1) in the socket grey of menu chrome.
	legendStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(colorSocket)).MarginTop(1)
)

// InitStyles overrides the default palette from the environment so colors can be
// tuned in .env without recompiling. It runs after config.Load has populated the
// environment; env values are ANSI codes (0-255), hex (#rrggbb) or color names.
func InitStyles() {
	titleStyle = titleStyle.Foreground(envColor("COLOR_TITLE", colorSet))
	cursorStyle = cursorStyle.Foreground(envColor("COLOR_CURSOR", colorSocket))
	errStyle = errStyle.Foreground(envColor("COLOR_ERR", "1"))
	okStyle = okStyle.Foreground(envColor("COLOR_OK", "2"))
	warnStyle = warnStyle.Foreground(envColor("COLOR_WARN", "3"))
	textStyle = textStyle.Foreground(envColor("COLOR_TEXT", colorCoffee))
	selectedStyle = selectedStyle.Foreground(envColor("COLOR_SELECTED", colorSelBest))
	goldStyle = goldStyle.Foreground(envColor("COLOR_BALANCER", colorGold))
	bestStyle = bestStyle.Foreground(envColor("COLOR_BEST", colorBest))
	legendStyle = legendStyle.Foreground(envColor("COLOR_MENU", colorSocket))
}

func envColor(key, def string) lipgloss.Color {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return lipgloss.Color(v)
	}
	return lipgloss.Color(def)
}

// emojiRanges are the rune blocks a panel draws its names with: flags, coloured
// circles, weather, hearts, dingbats — plus the joiners that glue a sequence
// together (VS15/VS16, ZWJ, skin tones, keycap). Go has no emoji table in the
// standard library and one subscription list does not justify pulling a package
// in, so the blocks are listed here.
var emojiRanges = [][2]rune{
	{0x200D, 0x200D},   // zero-width joiner
	{0x20E3, 0x20E3},   // combining keycap
	{0x2190, 0x21FF},   // arrows
	{0x2300, 0x23FF},   // misc technical (⌚ ⏳ …)
	{0x2600, 0x27BF},   // misc symbols + dingbats (☀ ★ ✅ ➡ …)
	{0x2B00, 0x2BFF},   // arrows/symbols (⭐ ⬛ …)
	{0xFE0E, 0xFE0F},   // variation selectors 15/16
	{0x1F000, 0x1FAFF}, // the pictograph planes, flags and skin tones included
}

func isEmoji(r rune) bool {
	for _, rg := range emojiRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// stripEmoji removes emoji from a display name. A leading run goes entirely,
// together with the separator it was pinned to ("🇩🇪 | DE-01" → "DE-01"); a run
// inside the name collapses to one space; a trailing run just goes. Windows
// conhost draws them as blank boxes, and their width is unknowable anyway — a
// name that is only letters is a name the table can align.
func stripEmoji(s string) string {
	var b strings.Builder
	led := false // an emoji opened the name, so its orphaned separator goes too
	for i, r := range s {
		switch {
		case !isEmoji(r):
			b.WriteRune(r)
		case i == 0:
			led = true
			fallthrough
		default:
			// One space per run, not per rune: 🇩🇪🇳🇱 must not become two gaps.
			if !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if led {
		out = strings.TrimLeft(out, "|-–—·•/\\ ")
	}
	// Collapse the gaps the removed runs left behind, so the column reads evenly.
	return strings.Join(strings.Fields(out), " ")
}

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

// pad right-pads s to w display columns. Names reach here through stripEmoji,
// so every rune left is one lipgloss can measure honestly — but %-*s counts
// bytes, and Cyrillic alone is enough to misalign that.
func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// padLeft left-pads s to w display columns, so numbers line up on their right
// edge in a table column.
func padLeft(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return strings.Repeat(" ", w-n) + s
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
	start = max(cursor-height/2, 0)
	if start+height > total {
		start = total - height
	}
	end = start + height
	return start, end, start, total - end
}

// rowBudget is how many list rows fit on screen once the fixed chrome is
// reserved (task #3): title(2) + header(1) + legend + two scroll indicators +
// the one-line notice. The notice line is reserved even while empty, so starting
// a ping does not shrink the list out from under the cursor (task #4). extra
// counts screen-specific lines, like the filter row. Both list screens share
// this so the two never drift apart. height<=0 (size unknown) shows everything.
func rowBudget(height, width int, keys string, total, extra int) int {
	if height <= 0 {
		return total
	}
	return max(1, height-(2+1+legendHeight(width, keys)+2+1+extra))
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
