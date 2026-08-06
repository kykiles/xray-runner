package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Task #5: a success shows, dims one rung per tick and goes. The ticks are fed
// straight in rather than through their timers — waiting out the real hold
// would cost seconds on every test run to prove what tea.Tick already types.
func TestNotice_SuccessFadesOut(t *testing.T) {
	var n notice
	const text = "Подписка добавлена"
	if cmd := n.ok(text); cmd == nil {
		t.Fatal("a fading notice must schedule its own removal")
	}
	if n.view() == "" {
		t.Fatal("notice is blank the moment it is set")
	}

	for step := 1; step <= len(noticeFade); step++ {
		if cmd := n.tick(noticeTickMsg{gen: n.gen}); cmd == nil {
			t.Fatalf("rung %d: notice stopped scheduling while still on screen", step)
		}
		if n.empty() {
			t.Fatalf("rung %d: notice vanished instead of dimming", step)
		}
		// Dimming, not rewriting: the words stay, the colour around them changes.
		if !strings.Contains(n.view(), text) {
			t.Errorf("rung %d rendered %q, want the text intact", step, n.view())
		}
	}

	if cmd := n.tick(noticeTickMsg{gen: n.gen}); cmd != nil {
		t.Error("a spent notice scheduled another tick")
	}
	if !n.empty() || n.view() != "" {
		t.Errorf("notice outlived its ramp: %q", n.view())
	}
}

// Failures go too (audit #2/#3): the screen stays minimal and the reason is in
// the log, so a warning about a cooldown does not sit there until the next key.
func TestNotice_FailureFadesOut(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(n *notice) tea.Cmd
	}{
		{"fail", func(n *notice) tea.Cmd { return n.fail("Не удалось обновить подписку") }},
		{"warn", func(n *notice) tea.Cmd { return n.warn("Протокол ssr не поддерживается") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var n notice
			cmd := c.set(&n)
			if cmd == nil {
				t.Fatal("a failure scheduled no fade")
			}
			if n.view() == "" {
				t.Fatal("notice is blank the moment it is set")
			}
			// Walk the whole ramp: one tick to start it, one per rung, one to blank.
			for range len(noticeFade) + 2 {
				n.tick(noticeTickMsg{gen: n.gen})
			}
			if n.view() != "" {
				t.Errorf("failure still on screen after the whole ramp: %q", n.view())
			}
		})
	}
}

// A replaced notice keeps its ticks to itself: the old message's countdown must
// not blank the one that took its place.
func TestNotice_StaleTickLeavesTheSuccessorAlone(t *testing.T) {
	var n notice
	n.ok("Пинг завершён")
	stale := noticeTickMsg{gen: n.gen}

	n.ok("Подписка обновлена: 42 сервера")
	for range len(noticeFade) + 2 {
		n.tick(stale)
	}
	if !strings.Contains(n.view(), "42") {
		t.Errorf("stale ticks ate the new notice: %q", n.view())
	}
}

// The whole point of clear: the line goes now, not in two seconds.
func TestNotice_ClearIsImmediate(t *testing.T) {
	var n notice
	n.ok("Пинг завершён")
	n.clear()
	if !n.empty() || n.view() != "" {
		t.Errorf("clear left %q", n.view())
	}
}
