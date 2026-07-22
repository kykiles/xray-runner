package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

var filterFixture = []subscription.SubEntry{
	{Remarks: "🇩🇪 Германия 1", Address: "de1.example.ru", Protocol: "vless", Network: "ws"},
	{Remarks: "🇩🇪 Германия 2", Address: "de2.example.ru", Protocol: "vless", Network: "tcp"},
	{Remarks: "🇺🇸 США", Address: "ws-node.example.com", Protocol: "ss", Network: "tcp"},
	{Remarks: "🇳🇱 Нидерланды", Address: "nl1.example.ru", Protocol: "vmess", Network: "ws"},
}

func matching(t *testing.T, query string) []string {
	t.Helper()
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []string
	for _, e := range filterFixture {
		if len(terms) == 0 || matchAll(e, terms) {
			out = append(out, e.Address)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The filter is a plain substring search spanning every column (task #1): one
// term finds the protocol, another the transport, another part of the name.
func TestFilter_SubstringAcrossColumns(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		// Protocol column.
		{"vless", []string{"de1.example.ru", "de2.example.ru"}},
		// Transport column AND the host that happens to spell "ws" — both match now.
		{"ws", []string{"de1.example.ru", "ws-node.example.com", "nl1.example.ru"}},
		// Name column.
		{"герман", []string{"de1.example.ru", "de2.example.ru"}},
		// Host column.
		{"nl1", []string{"nl1.example.ru"}},
		// Multiple terms narrow (AND): vless + ws transport → only de1.
		{"vless ws", []string{"de1.example.ru"}},
		// Terms may land in different columns: name + transport.
		{"герман tcp", []string{"de2.example.ru"}},
		// No match.
		{"zzz", nil},
	}

	for _, tt := range tests {
		if got := matching(t, tt.query); !equal(got, tt.want) {
			t.Errorf("filter %q = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestFilter_EmptyAndCaseInsensitive(t *testing.T) {
	if got := matching(t, ""); len(got) != len(filterFixture) {
		t.Errorf("empty filter = %v, want everything", got)
	}
	if got := matching(t, "  VLESS  "); !equal(got, []string{"de1.example.ru", "de2.example.ru"}) {
		t.Errorf("uppercase/padded filter = %v", got)
	}
}

func TestFlagSpace(t *testing.T) {
	tests := []struct{ in, want string }{
		{"🇩🇪Германия", "🇩🇪 Германия"},
		{"🇩🇪  Германия", "🇩🇪 Германия"},
		{"🇩🇪 Германия", "🇩🇪 Германия"},
		{"Германия", "Германия"}, // no flag, untouched
		{"🇩🇪", "🇩🇪"},             // flag only
	}
	for _, tt := range tests {
		if got := flagSpace(tt.in); got != tt.want {
			t.Errorf("flagSpace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A fixed filter narrows the ping to what is on screen, and the results still
// land on the right entries: the benchmark indexes the slice it got, so the
// indices must be mapped back to the full list.
func TestServersBenchmarkOnlyFiltered(t *testing.T) {
	var benched []string
	m := serversModel{
		ctx:     context.Background(),
		entries: filterFixture,
		filter:  textinput.New(),
		results: map[int]subscription.BenchmarkResult{},
		bench: func(_ context.Context, entries []subscription.SubEntry, on func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
			for i, e := range entries {
				benched = append(benched, e.Address)
				on(subscription.BenchmarkResult{Index: i, Latency: time.Millisecond})
			}
			return nil
		},
	}
	m.filter.SetValue("ws")

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	sm := next.(serversModel)

	for r := range sm.benchCh {
		sm.results[r.Index] = r
	}

	// "ws" matches the two ws transports and the ws-node host.
	if want := []string{"de1.example.ru", "ws-node.example.com", "nl1.example.ru"}; !equal(benched, want) {
		t.Errorf("benchmarked %v, want %v", benched, want)
	}
	for _, idx := range []int{0, 2, 3} {
		if _, ok := sm.results[idx]; !ok {
			t.Errorf("no result for entry %d", idx)
		}
	}
	if _, ok := sm.results[1]; ok {
		t.Error("entry 1 was filtered out but got a result")
	}
	if sm.benchTotal != 3 {
		t.Errorf("benchTotal = %d, want 3", sm.benchTotal)
	}
}

// The first row must stay on the same line while scrolling: at either end of
// the list one scroll indicator has nothing to report, and dropping its line
// used to pull the whole list up by one.
func TestServersView_RowsDoNotShiftWhileScrolling(t *testing.T) {
	entries := make([]subscription.SubEntry, 40)
	for i := range entries {
		entries[i] = subscription.SubEntry{
			Remarks: "srv", Address: "host.example.com", Port: 443,
			Protocol: "vless", Network: "tcp",
		}
	}

	want := -1
	for _, cursor := range []int{0, 1, 20, 38, 39} {
		m := serversModel{
			entries: entries,
			cursor:  cursor,
			filter:  textinput.New(),
			results: map[int]subscription.BenchmarkResult{},
			width:   120,
			height:  24,
		}
		lines := strings.Split(m.View(), "\n")
		got := -1
		for i, l := range lines {
			if strings.Contains(l, "host.example.com") {
				got = i
				break
			}
		}
		if got < 0 {
			t.Fatalf("cursor %d: no server row rendered", cursor)
		}
		if want < 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("cursor %d: first row on line %d, want %d", cursor, got, want)
		}
	}
}

// Task #4: pressing b used to grow the chrome by the ping-progress line, which
// shrank the row budget and pushed the bottom servers behind the scroll. The
// status block is reserved whether or not it has anything to say, so the row
// count must not change when a benchmark starts — and the view must still fit.
func TestServersView_BenchmarkKeepsRowCount(t *testing.T) {
	entries := make([]subscription.SubEntry, 40)
	for i := range entries {
		entries[i] = subscription.SubEntry{
			Remarks: "srv", Address: "host.example.com", Port: 443,
			Protocol: "vless", Network: "tcp",
		}
	}
	model := func(benching bool) serversModel {
		return serversModel{
			entries: entries,
			filter:  textinput.New(),
			results: map[int]subscription.BenchmarkResult{},
			bench: func(context.Context, []subscription.SubEntry, func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
				return nil
			},
			benching: benching,
			width:    120,
			height:   24,
		}
	}

	rows := func(out string) int {
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "host.example.com") {
				n++
			}
		}
		return n
	}

	idle, busy := model(false).View(), model(true).View()
	if a, b := rows(idle), rows(busy); a != b {
		t.Errorf("benchmark changed the row count: %d idle, %d while benching", a, b)
	}
	for name, out := range map[string]string{"idle": idle, "benching": busy} {
		if n := strings.Count(out, "\n"); n > 24 {
			t.Errorf("%s view is %d lines on a 24-line terminal:\n%s", name, n, out)
		}
	}
}

// Returning to the list lands the cursor on the server connected to last time,
// matched by address:port.
func TestSelectServer_CursorOnLastConnected(t *testing.T) {
	entries := []subscription.SubEntry{
		{Address: "de1.example.ru", Port: 443},
		{Address: "nl1.example.ru", Port: 443},
		{Address: "nl1.example.ru", Port: 8443},
	}
	tests := []struct {
		name    string
		address string
		port    int
		want    int
	}{
		{"match", "nl1.example.ru", 443, 1},
		{"port distinguishes", "nl1.example.ru", 8443, 2},
		{"no saved server", "", 0, 0},
		{"gone from subscription", "old.example.ru", 443, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indexOfServer(entries, tt.address, tt.port); got != tt.want {
				t.Errorf("indexOfServer(%q, %d) = %d, want %d", tt.address, tt.port, got, tt.want)
			}
		})
	}
}

// H-1: refreshing mid-benchmark used to swap the entry list out from under the
// running measurement, so results streaming in with the old indices landed on
// whatever server now sat at that position.
func TestServers_RefreshBlockedWhileBenching(t *testing.T) {
	m := serversModel{
		entries:  filterFixture,
		results:  map[int]subscription.BenchmarkResult{},
		filter:   textinput.New(),
		benching: true,
		refresh: func() ([]subscription.SubEntry, error) {
			t.Fatal("refresh must not start while a benchmark is running")
			return nil, nil
		},
	}

	got, cmd := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		// Run it so a wrongly-issued refresh trips the t.Fatal above.
		cmd()
	}
	if got.(serversModel).refreshing {
		t.Error("refresh started while benching")
	}
}

// Task #1: ping results share one right-aligned PING column, so short and long
// host names must not shift the numbers sideways.
func TestServersView_PingColumnAligns(t *testing.T) {
	entries := []subscription.SubEntry{
		{Remarks: "a", Address: "s.io", Port: 443, Protocol: "vless", Network: "tcp"},
		{Remarks: "b", Address: "very-long-host-name.example.com", Port: 8443, Protocol: "vless", Network: "ws"},
	}
	m := serversModel{
		entries: entries,
		filter:  textinput.New(),
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

// Task #2: a finished ping must not reshuffle the list — the subscription's own
// order is the intended one — and the fastest server is called out by color.
func TestServers_PingKeepsOrderAndMarksBest(t *testing.T) {
	m := serversModel{
		entries: filterFixture,
		filter:  textinput.New(),
		results: map[int]subscription.BenchmarkResult{
			0: {Index: 0, Latency: 300 * time.Millisecond},
			1: {Index: 1, Latency: 50 * time.Millisecond},
			2: {Index: 2, Error: errors.New("timeout")},
			3: {Index: 3, Latency: 120 * time.Millisecond},
		},
	}
	got, _ := m.Update(benchDoneMsg{})
	final := got.(serversModel)

	for pos, idx := range final.visible() {
		if pos != idx {
			t.Fatalf("ping reordered the list: position %d holds entry %d", pos, idx)
		}
	}
	if best := bestResult(final.results); best != 1 {
		t.Errorf("bestResult = %d, want 1 (the 50ms server)", best)
	}
	// A failed measurement never wins, even as the only one.
	if best := bestResult(map[int]subscription.BenchmarkResult{2: {Error: errors.New("x")}}); best != -1 {
		t.Errorf("bestResult over failures only = %d, want -1", best)
	}
}

// c opens the config viewer on the server under the cursor, ↓ scrolls it and ←
// returns to the list without leaving the screen.
func TestConfigViewer_OpenScrollClose(t *testing.T) {
	m := serversModel{
		entries: []subscription.SubEntry{
			{Remarks: "de", Address: "de1.example.ru", Port: 443, Protocol: "vless", UUID: "u-1", Network: "ws"},
		},
		results: map[int]subscription.BenchmarkResult{},
		filter:  textinput.New(),
		height:  10,
		width:   80,
		preview: func(e *subscription.SubEntry) (string, error) {
			return fmt.Sprintf("{\n  \"routing\": {},\n  \"host\": %q,\n  \"dns\": {}\n}", e.Address), nil
		},
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	view := next.(serversModel)
	if !view.cfg.open() {
		t.Fatalf("c did not open the config viewer; status: %q", view.status)
	}
	if out := view.View(); !strings.Contains(out, `"routing"`) || !strings.Contains(out, "конфиг") {
		t.Fatalf("config view missing config data:\n%s", out)
	}

	next, _ = view.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(serversModel).cfg.top; got != 1 {
		t.Fatalf("cfg.top after ↓ = %d, want 1", got)
	}

	next, _ = next.(serversModel).updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	back := next.(serversModel)
	if back.cfg.open() || back.action == ServerQuit && back.choice >= 0 {
		t.Fatalf("← did not return to the list")
	}
}

// The profile screen opens the same viewer, including on a single-server
// profile — the row that has no server screen to open it from.
func TestProfileConfigViewer_OpenClose(t *testing.T) {
	m := profilesModel{
		profiles: []subscription.Profile{{Name: "solo", Entries: []subscription.SubEntry{{Address: "de1.example.ru"}}}},
		height:   10,
		width:    80,
		preview:  func(subscription.Profile) (string, error) { return "{\n  \"dns\": {},\n  \"routing\": {}\n}", nil },
	}

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	view := next.(profilesModel)
	if !view.cfg.open() {
		t.Fatalf("c did not open the config viewer; status: %q", view.status)
	}
	if out := view.View(); !strings.Contains(out, `"dns"`) || !strings.Contains(out, "solo · конфиг") {
		t.Fatalf("profile config view missing data:\n%s", out)
	}

	next, _ = view.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if back := next.(profilesModel); back.cfg.open() || back.action == ProfileBack {
		t.Fatalf("← closed the screen instead of the viewer (action=%v)", back.action)
	}
}

// Coming back from a session restores the filter, and the cursor must land on
// the connected server counted among the *visible* rows.
func TestServers_CursorWithRestoredFilter(t *testing.T) {
	fi := textinput.New()
	fi.SetValue("ws")
	m := serversModel{entries: filterFixture, filter: fi}
	// "ws" leaves de1 (transport), ws-node (host) and nl1 (transport).
	if got := m.cursorAt("nl1.example.ru", 0); got != 2 {
		t.Errorf("cursor = %d, want 2", got)
	}
	// Filtered out: fall back to the top instead of pointing at another server.
	if got := m.cursorAt("de2.example.ru", 0); got != 0 {
		t.Errorf("cursor for a filtered-out server = %d, want 0", got)
	}
}
