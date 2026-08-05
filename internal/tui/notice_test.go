package tui

import (
	"strings"
	"testing"
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

// Errors stay: losing the reason a subscription would not load after three
// seconds is worse than a line left on screen.
func TestNotice_FailureDoesNotFade(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(n *notice)
	}{
		{"fail", func(n *notice) { n.fail("Ошибка обновления: нет сети") }},
		{"warn", func(n *notice) { n.warn("Протокол ssr не поддерживается") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var n notice
			c.set(&n)
			before := n.view()
			if before == "" {
				t.Fatal("notice is blank the moment it is set")
			}
			// Even handed a tick, it stays put and schedules nothing further.
			if cmd := n.tick(noticeTickMsg{gen: n.gen}); cmd != nil {
				t.Error("a failure scheduled a fade")
			}
			if n.view() != before {
				t.Errorf("failure changed on a tick: %q -> %q", before, n.view())
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
