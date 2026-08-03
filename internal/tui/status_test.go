package tui

import (
	"strings"
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

// The empty split list used to arrive as a sticky note, so a process started
// mid-session joined the tunnel while the screen still said nothing was running.
// Now the row is live: it follows the rescan updates in both directions.
func TestStatus_ProcessRowFollowsRescan(t *testing.T) {
	m := statusModel{info: StatusInfo{Split: true}, updates: make(chan StatusUpdate, 1)}

	if !strings.Contains(m.View(), "нет запущенных") {
		t.Fatal("empty split list should say nothing is running")
	}

	got, _ := m.Update(StatusUpdate{Apps: []string{"claude"}})
	m = got.(statusModel)
	if view := m.View(); strings.Contains(view, "нет запущенных") || !strings.Contains(view, "claude") {
		t.Fatalf("after rescan the row should list claude, got:\n%s", view)
	}

	got, _ = m.Update(StatusUpdate{Apps: []string{}})
	if view := got.(statusModel).View(); !strings.Contains(view, "нет запущенных") {
		t.Fatalf("an emptied list should go back to the empty text, got:\n%s", view)
	}
}

// The mode row is named by what is actually happening: SPLIT only while a
// listed process is captured, PROXY before and after.
func TestStatus_ModeRowFollowsSplit(t *testing.T) {
	m := statusModel{
		info:    StatusInfo{Split: true, Mode: "PROXY (127.0.0.1:10809)", SplitMode: "SPLIT (127.0.0.1:10809)"},
		updates: make(chan StatusUpdate, 1),
	}

	if strings.Contains(m.View(), "SPLIT") {
		t.Fatal("no captured process yet — the row must still say PROXY")
	}

	got, _ := m.Update(StatusUpdate{Apps: []string{"claude"}})
	m = got.(statusModel)
	if view := m.View(); !strings.Contains(view, "SPLIT") {
		t.Fatalf("a captured process should rename the mode to SPLIT, got:\n%s", view)
	}

	got, _ = m.Update(StatusUpdate{Apps: []string{}})
	if view := got.(statusModel).View(); strings.Contains(view, "SPLIT") {
		t.Fatalf("an emptied list should go back to PROXY, got:\n%s", view)
	}
}

// The counter measures the live connection: nothing is shown while the screen
// says "подключение", and the clock starts on the first successful check.
func TestUptimeStartsOnFirstOK(t *testing.T) {
	m := statusModel{}

	if got := m.health(); strings.Contains(got, "uptime") {
		t.Errorf("health() = %q, want no uptime before the first check", got)
	}

	failed, _ := m.Update(StatusUpdate{OK: false})
	m = failed.(statusModel)
	if got := m.health(); strings.Contains(got, "uptime") {
		t.Errorf("health() = %q, want no uptime while the connection is down", got)
	}

	ok, _ := m.Update(StatusUpdate{OK: true})
	m = ok.(statusModel)
	if got := m.health(); !strings.Contains(got, "uptime") {
		t.Errorf("health() = %q, want an uptime once connected", got)
	}
}
