package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

const validUUID = "123e4567-e89b-12d3-a456-426614174000"

// balancerFixture is a subscription with balancers: a two-server balancer, a
// single-server profile, and a one-server balancer.
func balancerFixture() []subscription.Profile {
	return []subscription.Profile{
		{Name: "Германия", Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}, Entries: []subscription.SubEntry{
			{Remarks: "🇩🇪 DE-1", Address: "de1.example.ru", Port: 443, Protocol: "vless", Network: "ws", UUID: validUUID},
			{Remarks: "🇩🇪 DE-2", Address: "de2.example.ru", Port: 443, Protocol: "vless", Network: "ws", UUID: validUUID},
		}},
		{Name: "США", Entries: []subscription.SubEntry{
			{Remarks: "US", Address: "us1.example.com", Port: 443, Protocol: "vmess", Network: "tcp", UUID: validUUID},
		}},
		{Name: "Нидерланды", Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}, Entries: []subscription.SubEntry{
			{Remarks: "NL-1", Address: "nl1.example.ru", Port: 443, Protocol: "vless", Network: "tcp", UUID: validUUID},
		}},
	}
}

// flatFixture is a subscription with no balancers: a plain server list.
func flatFixture() []subscription.Profile {
	return []subscription.Profile{
		{Name: "Германия", Entries: []subscription.SubEntry{{Remarks: "DE", Address: "de1.example.ru", Port: 443, Protocol: "vless", Network: "ws", UUID: validUUID}}},
		{Name: "США", Entries: []subscription.SubEntry{{Remarks: "US", Address: "us1.example.com", Port: 443, Protocol: "vmess", Network: "tcp", UUID: validUUID}}},
	}
}

func newList(profiles []subscription.Profile) listModel {
	return listModel{
		ctx:       context.Background(),
		profiles:  profiles,
		flat:      subscription.AllSingle(profiles),
		expanded:  map[int]bool{},
		filter:    newFilter(),
		pings:     PingCache{},
		profPings: PingCache{},
		action:    ListQuit,
		choice:    -1,
		width:     200,
		height:    24,
	}
}

func press(m listModel, s string) listModel {
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(listModel)
}

// A subscription without balancers renders a plain server list: no unfold key,
// and → connects the server under the cursor.
func TestList_FlatRendersPlainServers(t *testing.T) {
	m := newList(flatFixture())
	if !m.flat {
		t.Fatal("all-single subscription should be flat")
	}
	rows := m.rows()
	if len(rows) != 2 || rows[0].kind != rowServer || rows[1].kind != rowServer {
		t.Fatalf("flat rows = %+v, want two servers", rows)
	}
	out := m.View()
	if strings.Contains(out, "s серверы") {
		t.Errorf("flat list must not offer the unfold key:\n%s", out)
	}
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	final := next.(listModel)
	if final.action != ListConnect || final.entry == nil || final.entry.Address != "de1.example.ru" {
		t.Errorf("→ on a flat server: action=%v entry=%+v, want connect de1", final.action, final.entry)
	}
}

// A subscription with balancers renders the tree: balancer rows offer the
// unfold key, s unfolds them in place, → runs a balancer as a whole, and →
// connects a server (child or single-server profile).
func TestList_BalancerTreeFoldAndActions(t *testing.T) {
	m := newList(balancerFixture())
	if m.flat {
		t.Fatal("a subscription with balancers is not flat")
	}
	// Cursor on the balancer row offers the unfold key.
	if !strings.Contains(m.View(), "s серверы") {
		t.Errorf("balancer row must offer the unfold key:\n%s", m.View())
	}
	// Top-level rows: balancer, server (single-server profile), balancer.
	rows := m.rows()
	if len(rows) != 3 || rows[0].kind != rowBalancer || rows[1].kind != rowServer || rows[2].kind != rowBalancer {
		t.Fatalf("top rows = %+v", rows)
	}

	// s unfolds the first balancer: its two servers appear under it.
	folded := len(m.visibleRows())
	m = press(m, "s")
	if got := len(m.visibleRows()); got != folded+2 {
		t.Fatalf("unfold added %d rows, want 2", got-folded)
	}
	if out := m.View(); !strings.Contains(out, "de1.example.ru") || !strings.Contains(out, "de2.example.ru") {
		t.Errorf("unfolded servers not shown:\n%s", out)
	}

	// → on the balancer runs it as a whole.
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if final := next.(listModel); final.action != ListRunBalancer || final.choice != 0 {
		t.Errorf("→ on balancer: action=%v choice=%d, want run/0", final.action, final.choice)
	}

	// → on a child server connects it, tagged with its balancer's profile index.
	m.cursor = 1 // first child of the unfolded balancer
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	final := next.(listModel)
	if final.action != ListConnect || final.entry == nil || final.entry.Address != "de1.example.ru" || final.choice != 0 {
		t.Errorf("→ on child: action=%v entry=%+v choice=%d", final.action, final.entry, final.choice)
	}

	// A single-server profile connects directly, under its own profile index.
	m2 := newList(balancerFixture())
	m2.cursor = 1 // the США server row
	next, _ = m2.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if final := next.(listModel); final.action != ListConnect || final.choice != 1 || final.entry.Address != "us1.example.com" {
		t.Errorf("→ on single-server profile: action=%v choice=%d entry=%+v", final.action, final.choice, final.entry)
	}
}

// The filter narrows the tree over the same columns the table shows, and the
// cursor counts visible rows so → acts on the row it points at.
func TestList_FilterNarrowsAndKeepsChoice(t *testing.T) {
	m := newList(balancerFixture())
	m = press(m, "/")
	if !m.filter.typing {
		t.Fatal("/ did not open the filter")
	}
	for _, r := range "нидер" {
		m = press(m, string(r))
	}
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(listModel)

	vis := m.visibleRows()
	if len(vis) != 1 || vis[0].name != "Нидерланды" {
		t.Fatalf("visible = %+v, want just Нидерланды", vis)
	}
	if strings.Contains(m.View(), "us1.example.com") {
		t.Errorf("filtered-out row still rendered:\n%s", m.View())
	}
	// → runs the one visible balancer (profile index 2 in the full list).
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if final := next.(listModel); final.action != ListRunBalancer || final.choice != 2 {
		t.Errorf("→ gave action %v choice %d, want run/2", final.action, final.choice)
	}
}

// drainBench runs the benchmark command to completion, routing every streamed
// result through Update so the caches fill exactly as they would on screen.
func drainBench(m listModel, cmd tea.Cmd) listModel {
	for cmd != nil {
		msg := cmd()
		next, nextCmd := m.Update(msg)
		m = next.(listModel)
		cmd = nextCmd
		if _, done := msg.(benchDoneMsg); done {
			break
		}
	}
	return m
}

// Variant A: b measures the visible balancers as whole profiles and every
// visible server one by one, in one run, routing each result to the right cache.
// A folded balancer costs one measurement, not N+1: the servers behind it are
// only measured once it is unfolded.
func TestList_BenchmarkVariantA(t *testing.T) {
	var srvSeen, profSeen []string
	m := newList(balancerFixture())
	m.srvBench = func(_ context.Context, entries []subscription.SubEntry, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
		for i, e := range entries {
			srvSeen = append(srvSeen, e.Address)
			on(subscription.BenchmarkResult{Index: i, Latency: 10 * time.Millisecond})
		}
		return nil
	}
	m.profBench = func(_ context.Context, ps []subscription.Profile, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
		for i, p := range ps {
			profSeen = append(profSeen, p.Name)
			on(subscription.BenchmarkResult{Index: i, Latency: 20 * time.Millisecond})
		}
		return nil
	}

	next, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = drainBench(next.(listModel), cmd)

	// Both balancers are folded, so only the standalone server is measured on its
	// own; each balancer is measured as a whole.
	if want := "us1.example.com"; strings.Join(srvSeen, ",") != want {
		t.Errorf("server bench saw %v, want %v", srvSeen, want)
	}
	if strings.Join(profSeen, ",") != "Германия,Нидерланды" {
		t.Errorf("profile bench saw %v, want [Германия Нидерланды]", profSeen)
	}
	if got := m.pings[pingKey(subscription.SubEntry{Address: "us1.example.com", Port: 443})]; got.Latency != 10*time.Millisecond {
		t.Errorf("server result not cached: %+v", got)
	}
	if got := m.profPings["Германия|de1.example.ru:443"]; got.Latency != 20*time.Millisecond {
		t.Errorf("balancer result not cached: %+v", got)
	}
	if m.run.running {
		t.Error("run still marked running after completion")
	}
}

// Unfolding a balancer puts its servers back into the run: they are visible
// rows now, and the row that used to carry the profile's number gives its cell
// up to them.
func TestList_BenchmarkMeasuresUnfoldedChildren(t *testing.T) {
	var srvSeen []string
	m := newList(balancerFixture())
	m.srvBench = func(_ context.Context, entries []subscription.SubEntry, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
		for i, e := range entries {
			srvSeen = append(srvSeen, e.Address)
			on(subscription.BenchmarkResult{Index: i, Latency: 10 * time.Millisecond})
		}
		return nil
	}

	m = press(m, "s") // unfold Германия, the cursor starts on it
	next, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	drainBench(next.(listModel), cmd)

	if want := "de1.example.ru,de2.example.ru,us1.example.com"; strings.Join(srvSeen, ",") != want {
		t.Errorf("server bench saw %v, want %v", srvSeen, want)
	}
}

// Task #2: the balancer's own number sits on its row while it is folded and
// gives the cell up once it is unfolded, where the servers behind it carry their
// own — and folding it back brings it again instead of leaving a hole.
func TestList_BalancerPingMovesToChildren(t *testing.T) {
	m := newList(balancerFixture())
	m.pings[pingKey(m.profiles[0].Entries[0])] = subscription.BenchmarkResult{Latency: 11 * time.Millisecond}
	m.pings[pingKey(m.profiles[0].Entries[1])] = subscription.BenchmarkResult{Latency: 22 * time.Millisecond}
	m.profPings[profileKey(m.profiles[0])] = subscription.BenchmarkResult{Latency: 33 * time.Millisecond}

	folded := m.View()
	if !strings.Contains(folded, "33ms") || strings.Contains(folded, "11ms") {
		t.Errorf("folded balancer must show its own ping and nothing else:\n%s", folded)
	}

	m = press(m, "s")
	open := m.View()
	if strings.Contains(open, "33ms") {
		t.Errorf("unfolded balancer must give its ping cell up:\n%s", open)
	}
	if !strings.Contains(open, "11ms") || !strings.Contains(open, "22ms") {
		t.Errorf("unfolded servers must carry their own pings:\n%s", open)
	}

	m = press(m, "s")
	if back := m.View(); !strings.Contains(back, "33ms") {
		t.Errorf("folding back must restore the balancer's ping:\n%s", back)
	}
}

// Task #1: a star beside the cursor marks a balancer, and only there.
func TestList_StarMarksBalancerUnderCursor(t *testing.T) {
	m := newList(balancerFixture())
	if line := cursorLine(m.View()); !strings.Contains(line, "★") {
		t.Errorf("cursor on a balancer, want a star:\n%q", line)
	}
	m.cursor = 1 // the США server row
	if line := cursorLine(m.View()); strings.Contains(line, "★") {
		t.Errorf("cursor on a server, want no star:\n%q", line)
	}
}

// cursorLine is the rendered line the cursor marker sits on.
func cursorLine(view string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "▸") {
			return l
		}
	}
	return ""
}

// Pressing b again restarts on what is on screen now; the cancelled run's late
// results are dropped by generation.
func TestList_BenchmarkRestartDropsOldRun(t *testing.T) {
	started := make(chan context.Context, 2)
	release := make(chan struct{})
	m := newList(flatFixture())
	m.srvBench = func(ctx context.Context, entries []subscription.SubEntry, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
		started <- ctx
		<-release
		on(subscription.BenchmarkResult{Index: 0, Latency: 999 * time.Millisecond})
		return nil
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	first := next.(listModel)
	firstCtx := <-started
	firstCh := first.run.ch

	first.filter.input.SetValue("us1")
	next, _ = first.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	second := next.(listModel)
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

	stale := <-firstCh
	after, _ := second.Update(benchResultMsg{gen: second.run.gen - 1, BenchmarkResult: stale})
	if got := after.(listModel); len(got.pings) != 0 || got.run.done != 0 {
		t.Errorf("stale result was accepted: pings=%v done=%d", got.pings, got.run.done)
	}
}

// Ping results share one right-aligned PING column, so short and long rows must
// not shift the numbers sideways.
func TestList_PingColumnAligns(t *testing.T) {
	m := newList(flatFixture())
	m.pings = PingCache{
		pingKey(m.profiles[0].Entries[0]): {Latency: 12 * time.Millisecond},
		pingKey(m.profiles[1].Entries[0]): {Latency: 1234 * time.Millisecond},
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

// c opens the config viewer on a server and on a balancer; ← returns to the
// list without leaving the screen.
func TestList_ConfigViewer(t *testing.T) {
	m := newList(balancerFixture())
	m.height, m.width = 12, 80
	m.srvPreview = func(e *subscription.SubEntry, _ int) (string, error) {
		return "{\n  \"routing\": {},\n  \"host\": \"" + e.Address + "\"\n}", nil
	}
	m.profPreview = func(p subscription.Profile) (string, error) {
		return "{\n  \"balancers\": [\"" + p.Name + "\"]\n}", nil
	}

	// On a balancer row.
	view := press(m, "c")
	if !view.cfg.open() {
		t.Fatalf("c did not open the viewer on a balancer; status %q", view.status)
	}
	if out := view.View(); !strings.Contains(out, "balancers") || !strings.Contains(out, "Германия · конфиг") {
		t.Fatalf("balancer config view missing data:\n%s", out)
	}
	back, _ := view.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if back.(listModel).cfg.open() {
		t.Fatal("← did not close the viewer")
	}

	// On the single-server profile row.
	m.cursor = 1
	view = press(m, "c")
	if out := view.View(); !strings.Contains(out, "routing") || !strings.Contains(out, "us1.example.com") {
		t.Fatalf("server config view missing data:\n%s", out)
	}
}

// Task #5: a finished-ping notice clears the moment the user keeps navigating,
// so it does not read as a fresh measurement; the pings themselves stay.
func TestList_BenchStatusClearsOnNavigation(t *testing.T) {
	m := newList(flatFixture())
	done, _ := m.Update(benchDoneMsg{})
	m = done.(listModel)
	if !strings.Contains(m.status, "Пинг завершён") {
		t.Fatalf("status after ping = %q, want the completion notice", m.status)
	}
	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(listModel).status; got != "" {
		t.Errorf("status after navigation = %q, want cleared", got)
	}
}

// r replaces the list and drops the numbers that belonged to the servers it
// replaced.
func TestList_RefreshReplacesAndDropsCaches(t *testing.T) {
	fresh := flatFixture()
	m := newList(balancerFixture())
	m.pings["de1.example.ru:443"] = subscription.BenchmarkResult{Latency: time.Millisecond}
	m.profPings["Германия|de1.example.ru:443"] = subscription.BenchmarkResult{Latency: time.Millisecond}
	m.refresh = func() ([]subscription.Profile, error) { return fresh, nil }

	next, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("r produced no refresh")
	}
	if !next.(listModel).refreshing {
		t.Error("refreshing not set")
	}
	done, _ := next.(listModel).Update(cmd())
	got := done.(listModel)
	if len(got.profiles) != 2 || !got.flat {
		t.Errorf("profiles not replaced / flat not recomputed: %+v flat=%v", got.profiles, got.flat)
	}
	if len(got.pings) != 0 || len(got.profPings) != 0 {
		t.Errorf("stale numbers kept: pings=%v profPings=%v", got.pings, got.profPings)
	}
}

// Task #5: r is the one button that always hits the panel, so holding it down
// must not turn into a request per keypress.
func TestList_RefreshCooldown(t *testing.T) {
	m := newList(flatFixture())
	m.refresh = func() ([]subscription.Profile, error) { return flatFixture(), nil }

	r := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}
	next, cmd := m.updateKey(r)
	if cmd == nil {
		t.Fatal("the first r did not refresh")
	}
	done, _ := next.(listModel).Update(cmd())
	m = done.(listModel)

	if _, cmd := m.updateKey(r); cmd != nil {
		t.Error("r inside the cooldown fetched again")
	}
	m.lastRefresh = time.Now().Add(-refreshCooldown)
	if _, cmd := m.updateKey(r); cmd == nil {
		t.Error("r after the cooldown must fetch again")
	}
}

// Coming back from a session unfolds the balancer holding the last connected
// server and lands the cursor on it.
func TestList_RestoreCursorUnfoldsChild(t *testing.T) {
	m := newList(balancerFixture())
	m.restoreCursor("de2.example.ru", 443, -1)
	if !m.expanded[0] {
		t.Fatal("the balancer holding the last server was not unfolded")
	}
	vis := m.visibleRows()
	if m.cursor >= len(vis) || vis[m.cursor].entry.Address != "de2.example.ru" {
		t.Errorf("cursor landed on %+v, want de2.example.ru", vis[m.cursor])
	}
}

// The first row keeps its line while scrolling, and starting a benchmark does
// not change the row count — the status block is reserved either way.
func TestList_WindowStableWhileScrollingAndBenching(t *testing.T) {
	profiles := make([]subscription.Profile, 40)
	for i := range profiles {
		profiles[i] = subscription.Profile{Name: "srv", Entries: []subscription.SubEntry{
			{Remarks: "srv", Address: "host.example.com", Port: 443, Protocol: "vless", Network: "tcp", UUID: validUUID},
		}}
	}

	firstLine := -1
	for _, cursor := range []int{0, 1, 20, 38, 39} {
		m := newList(profiles)
		m.cursor = cursor
		lines := strings.Split(m.View(), "\n")
		got := -1
		for i, l := range lines {
			if strings.Contains(l, "host.example.com") {
				got = i
				break
			}
		}
		if got < 0 {
			t.Fatalf("cursor %d: no row rendered", cursor)
		}
		if firstLine < 0 {
			firstLine = got
		} else if got != firstLine {
			t.Errorf("cursor %d: first row on line %d, want %d", cursor, got, firstLine)
		}
	}

	rowsShown := func(benching bool) int {
		m := newList(profiles)
		m.srvBench = func(context.Context, []subscription.SubEntry, func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
			return nil
		}
		m.run = benchState{running: benching}
		n := 0
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.Contains(l, "host.example.com") {
				n++
			}
		}
		return n
	}
	if idle, busy := rowsShown(false), rowsShown(true); idle != busy {
		t.Errorf("benchmark changed the row count: %d idle, %d benching", idle, busy)
	}
}
