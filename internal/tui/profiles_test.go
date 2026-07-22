package tui

import (
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
