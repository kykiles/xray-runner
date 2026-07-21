package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

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
