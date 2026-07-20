package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFmtDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00"},
		{90 * time.Second, "00:01:30"},
		{25*time.Hour + 30*time.Minute, "1d 01:30:00"},
		{49 * time.Hour, "2d 01:00:00"},
	}
	for _, tt := range tests {
		if got := fmtDuration(tt.d); got != tt.want {
			t.Errorf("fmtDuration(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// H-2: every other screen doubles its letter hotkeys with the Russian-layout
// twin (task #7); the connected screen only had the Latin ones, which left a
// Russian-layout user with no way to quit, restart or switch mode.
func TestStatus_CyrillicHotkeys(t *testing.T) {
	cases := []struct {
		key  string
		want StatusAction
	}{
		{"q", StatusQuit}, {"й", StatusQuit},
		{"m", StatusSwitchMode}, {"ь", StatusSwitchMode},
		{"r", StatusRestart}, {"к", StatusRestart},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			m := statusModel{action: StatusBack}
			got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.key)})
			if cmd == nil {
				t.Fatalf("key %q produced no command, expected quit", c.key)
			}
			if act := got.(statusModel).action; act != c.want {
				t.Errorf("key %q → action %v, want %v", c.key, act, c.want)
			}
		})
	}
}
