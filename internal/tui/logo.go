// Package tui: the XRAY-RUNNER wordmark shown at the top of the subscription
// screen.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// logoFull is the wordmark from docs/logo.md. It carries no ANSI codes of its
// own: color comes from titleStyle at render time, so the logo follows
// COLOR_TITLE like the rest of the TUI and degrades on terminals without
// truecolor.
const logoFull = `
██╗  ██╗██████╗  █████╗ ██╗   ██╗     ██████╗ ██╗   ██╗███╗   ██╗███╗   ██╗███████╗██████╗
╚██╗██╔╝██╔══██╗██╔══██╗╚██╗ ██╔╝     ██╔══██╗██║   ██║████╗  ██║████╗  ██║██╔════╝██╔══██╗
 ╚███╔╝ ██████╔╝███████║ ╚████╔╝█████╗██████╔╝██║   ██║██╔██╗ ██║██╔██╗ ██║█████╗  ██████╔╝
 ██╔██╗ ██╔══██╗██╔══██║  ╚██╔╝ ╚════╝██╔══██╗██║   ██║██║╚██╗██║██║╚██╗██║██╔══╝  ██╔══██╗
██╔╝ ██╗██║  ██║██║  ██║   ██║        ██║  ██║╚██████╔╝██║ ╚████║██║ ╚████║███████╗██║  ██║
╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝        ╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚═══╝╚═╝  ╚═══╝╚══════╝╚═╝  ╚═╝`

// logoCompact is the same wordmark in a narrower face, for terminals too small
// for logoFull.
const logoCompact = `
█  █ ███   ██  █  █      ███  █  █ ██ █ ██ █ ████ ███
 ██  ██   ████  ██  ████ ██   █  █ █ ██ █ ██ ███  ██
█  █ █  █ █  █   █       █  █ ████ █  █ █  █ ████ █  █`

// minLogoHeight is the terminal height below which the six-row logo would crowd
// the subscription list off the screen.
const minLogoHeight = 16

// renderLogo picks the widest wordmark that fits, falling back to a plain title
// when the terminal is too short and to plain text when it is too narrow.
// width and height are 0 until the first WindowSizeMsg.
func renderLogo(width, height int) string {
	if height > 0 && height < minLogoHeight {
		return titleStyle.Render("Мои подписки")
	}
	for _, art := range []string{logoFull, logoCompact} {
		if width >= artWidth(art) {
			return titleStyle.Render(art)
		}
	}
	return titleStyle.Render("xray-runner")
}

// artWidth reports the display columns of the widest line in art.
func artWidth(art string) int {
	w := 0
	for _, line := range strings.Split(art, "\n") {
		if n := lipgloss.Width(line); n > w {
			w = n
		}
	}
	return w
}
