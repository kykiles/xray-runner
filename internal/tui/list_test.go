package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

var profileFixture = []subscription.Profile{
	{Name: "Германия", Entries: []subscription.SubEntry{{Address: "de1.example.ru", Protocol: "vless", Network: "ws"}},
		Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}},
	{Name: "США", Entries: []subscription.SubEntry{{Address: "us1.example.com", Protocol: "vmess", Network: "tcp"}}},
	{Name: "Нидерланды", Entries: []subscription.SubEntry{{Address: "nl1.example.ru", Protocol: "vless", Network: "tcp"}},
		Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}},
}

// Task #1: the profile list has the same filter as the server list — including
// on a subscription with balancers, where it used to be missing entirely.
func TestProfiles_FilterNarrowsAndKeepsChoice(t *testing.T) {
	m := profilesModel{
		ctx:      context.Background(),
		profiles: profileFixture,
		results:  map[int]subscription.BenchmarkResult{},
		filter:   newFilter(),
		choice:   -1,
		width:    200,
		height:   24,
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(profilesModel)
	if !m.filter.typing {
		t.Fatal("/ did not open the filter")
	}
	for _, r := range "нидер" {
		next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(profilesModel)
	}
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(profilesModel)

	if got := m.visible(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("visible = %v, want [2]", got)
	}
	if out := m.View(); strings.Contains(out, "us1.example.com") {
		t.Errorf("filtered-out profile still rendered:\n%s", out)
	}

	// The cursor counts visible rows; → must connect the profile it points at,
	// not the one sitting at that position in the full list.
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if final := next.(profilesModel); final.choice != 2 || final.action != ProfileRun {
		t.Errorf("→ gave choice %d action %v, want 2/run", final.choice, final.action)
	}

	// ← drops the filter first and only then leaves the screen.
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(profilesModel)
	if m.action == ProfileBack || m.filter.value() != "" {
		t.Errorf("← left the screen instead of clearing the filter")
	}
}

// The profile ping measures what the filter left on screen, and the results
// land on the right profiles.
func TestProfiles_BenchmarkOnlyFiltered(t *testing.T) {
	var benched []string
	m := profilesModel{
		ctx:      context.Background(),
		profiles: profileFixture,
		results:  map[int]subscription.BenchmarkResult{},
		filter:   newFilter(),
		bench: func(_ context.Context, ps []subscription.Profile, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
			for i, p := range ps {
				benched = append(benched, p.Name)
				on(subscription.BenchmarkResult{Index: i, Latency: time.Millisecond})
			}
			return nil
		},
	}
	m.filter.input.SetValue(".ru")

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	pm := next.(profilesModel)
	for r := range pm.run.ch {
		pm.results[r.Index] = r
	}

	if want := []string{"Германия", "Нидерланды"}; strings.Join(benched, ",") != strings.Join(want, ",") {
		t.Errorf("benchmarked %v, want %v", benched, want)
	}
	if _, ok := pm.results[1]; ok {
		t.Error("a filtered-out profile got a result")
	}
	if _, ok := pm.results[2]; !ok {
		t.Error("no result for profile 2")
	}
}

// Task #2: pressing b again restarts the measurement on what is on screen now.
// The old run is cancelled and whatever it still emits is dropped, instead of
// the screen sitting through the old ping and filling in stale numbers.
func TestBench_RestartDropsTheOldRun(t *testing.T) {
	started := make(chan context.Context, 2)
	release := make(chan struct{})
	m := serversModel{
		ctx:     context.Background(),
		entries: filterFixture,
		results: map[int]subscription.BenchmarkResult{},
		pings:   PingCache{},
		filter:  newFilter(),
		bench: func(ctx context.Context, entries []subscription.SubEntry, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
			started <- ctx
			<-release
			on(subscription.BenchmarkResult{Index: 0, Latency: 999 * time.Millisecond})
			return nil
		},
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	first := next.(serversModel)
	firstCtx := <-started
	firstCh := first.run.ch

	// Change the filter and ping again.
	first.filter.input.SetValue("nl1")
	next, _ = first.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	second := next.(serversModel)
	<-started
	close(release)

	if firstCtx.Err() == nil {
		t.Error("the first run was not cancelled")
	}
	if second.run.ch == firstCh {
		t.Fatal("the second run reused the first channel")
	}
	if second.run.total != 1 {
		t.Errorf("total = %d, want 1 (only the filtered server)", second.run.total)
	}

	// A result from the cancelled run must not land in the new one.
	stale := <-firstCh
	after, _ := second.Update(benchResultMsg{gen: second.run.gen - 1, BenchmarkResult: stale})
	if got := after.(serversModel); len(got.results) != 0 || got.run.done != 0 {
		t.Errorf("stale result was accepted: results=%v done=%d", got.results, got.run.done)
	}
}
