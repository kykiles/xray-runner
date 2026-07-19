package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

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
			order:   identityOrder(len(entries)),
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
