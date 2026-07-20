package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// renderLogo must never emit a line wider than the terminal, or the wordmark
// wraps and turns to noise.
func TestRenderLogo(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          string
	}{
		{"wide terminal shows the full logo", 120, 30, "██████╗"},
		{"exact fit shows the full logo", artWidth(logoFull), 30, "██████╗"},
		{"narrow terminal shows the compact logo", 60, 30, "██ █ ██ █"},
		{"too narrow for either falls back to text", 20, 30, "xray-runner"},
		{"unknown size falls back to text", 0, 0, "xray-runner"},
		{"short terminal keeps the plain title", 120, 10, "Мои подписки"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderLogo(tt.width, tt.height)
			if !strings.Contains(got, tt.want) {
				t.Errorf("renderLogo(%d, %d) = %q, want it to contain %q",
					tt.width, tt.height, got, tt.want)
			}
			if tt.width <= 0 {
				return
			}
			for _, line := range strings.Split(got, "\n") {
				if n := lipgloss.Width(line); n > tt.width {
					t.Errorf("renderLogo(%d, %d) emitted a %d-column line: %q",
						tt.width, tt.height, n, line)
				}
			}
		})
	}
}
