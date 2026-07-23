package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

// The profile list is the same table as the server list: a profile shows the
// protocol/transport/host of its first server, and only a balancer row offers
// the "unfold servers" key.
func TestProfilesView_SharedColumnsAndExpandKey(t *testing.T) {
	m := profilesModel{
		profiles: []subscription.Profile{
			{Name: "single", Entries: []subscription.SubEntry{{Protocol: "vless", Network: "ws", Address: "a.example", Port: 443}}},
			{
				Name:     "balanced",
				Balancer: &subscription.BalancerInfo{Strategy: "leastPing"},
				Entries:  []subscription.SubEntry{{Protocol: "vmess", Network: "tcp", Address: "b.example", Port: 8443}},
			},
		},
		action: ProfileQuit,
		choice: -1,
		width:  200,
		height: 24,
	}

	out := m.View()
	for _, want := range []string{"PROTOCOL", "TRANSPORT", "HOST", "vless", "ws", "a.example:443"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s серверы") {
		t.Errorf("single profile must not offer the unfold key:\n%s", out)
	}

	// s does nothing on a single-server profile.
	if got, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}); got.(profilesModel).action != ProfileQuit {
		t.Errorf("s on a single profile: action = %v, want unchanged", got.(profilesModel).action)
	}

	m.cursor = 1
	if !strings.Contains(m.View(), "s серверы") {
		t.Errorf("balancer row must offer the unfold key:\n%s", m.View())
	}
	got, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if final := got.(profilesModel); final.action != ProfileExpand || final.choice != 1 {
		t.Errorf("s on a balancer: action = %v, choice = %d, want expand of 1", final.action, final.choice)
	}
	// → connects on both screens now.
	got, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if final := got.(profilesModel); final.action != ProfileRun {
		t.Errorf("→ on a balancer: action = %v, want run", final.action)
	}
}

// The profile list shares the servers table's right-aligned PING column, so a
// short mode ("single") and a long one must not shift the numbers sideways.
func TestProfilesView_PingColumnAligns(t *testing.T) {
	m := profilesModel{
		profiles: []subscription.Profile{
			{Name: "a"},
			{Name: "b", Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}},
		},
		results: map[int]subscription.BenchmarkResult{
			0: {Index: 0, Latency: 12 * time.Millisecond},
			1: {Index: 1, Latency: 1234 * time.Millisecond},
		},
		width:  200,
		height: 24,
	}
	out := m.View()
	if !strings.Contains(out, "PING") {
		t.Fatalf("no PING header:\n%s", out)
	}
	var cols []int
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "ms"); i >= 0 {
			cols = append(cols, lipgloss.Width(l[:i+2]))
		}
	}
	if len(cols) != 2 {
		t.Fatalf("want 2 ping cells, got %d:\n%s", len(cols), out)
	}
	if cols[0] != cols[1] {
		t.Errorf("ping cells end at columns %d and %d, want the same", cols[0], cols[1])
	}
}

// Coming back from a session restores the filter, and the cursor must land on
// the profile connected to — counted among the visible rows, not among all
// profiles.
func TestProfiles_RestoredFilterCursor(t *testing.T) {
	profiles := []subscription.Profile{
		{Name: "auto", Entries: []subscription.SubEntry{{Address: "a.example", Port: 443}}},
		{Name: "nl-1", Entries: []subscription.SubEntry{{Address: "b.example", Port: 443}}},
		{Name: "de", Entries: []subscription.SubEntry{{Address: "c.example", Port: 443}}},
		{Name: "nl-2", Entries: []subscription.SubEntry{{Address: "d.example", Port: 443}}},
	}
	fi := newFilter()
	fi.input.SetValue("nl")
	m := profilesModel{profiles: profiles, filter: fi}

	if got := m.visible(); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("visible = %v, want [1 3]", got)
	}
	// Profile 3 is the second visible row.
	if got := max(0, slices.Index(m.visible(), 3)); got != 1 {
		t.Errorf("cursor for profile 3 = %d, want 1", got)
	}
	// A profile filtered out falls back to the top instead of pointing past the end.
	if got := max(0, slices.Index(m.visible(), 2)); got != 0 {
		t.Errorf("cursor for a filtered-out profile = %d, want 0", got)
	}
}

// The profile screen keeps its measurements the same way the server screen
// does: pinging whole balancers is slow, and connecting and coming back used to
// wipe the column on exactly the subscriptions where the profile screen is the
// only place a ping can be taken.
func TestProfiles_PingCache(t *testing.T) {
	profiles := []subscription.Profile{
		{Name: "auto", Entries: []subscription.SubEntry{{Address: "a.example", Port: 443}}},
		{Name: "nl", Entries: []subscription.SubEntry{{Address: "b.example", Port: 443}}},
	}
	cache := PingCache{}
	m := profilesModel{
		profiles: profiles,
		results:  map[int]subscription.BenchmarkResult{},
		pings:    cache,
		filter:   newFilter(),
	}

	m.Update(benchResultMsg{BenchmarkResult: subscription.BenchmarkResult{Index: 1, Latency: 42 * time.Millisecond}})
	if got := cache[profileKey(profiles[1])]; got.Latency != 42*time.Millisecond {
		t.Fatalf("cache miss for profile 1: %+v", got)
	}

	// Reopening the screen — a refresh may have reordered the profiles.
	reordered := []subscription.Profile{profiles[1], profiles[0]}
	restored := profilePings(reordered, cache)
	if r, ok := restored[0]; !ok || r.Latency != 42*time.Millisecond || r.Index != 0 {
		t.Fatalf("restored[0] = %+v, ok=%v", r, ok)
	}
	if _, ok := restored[1]; ok {
		t.Error("an unmeasured profile got a result")
	}
}
