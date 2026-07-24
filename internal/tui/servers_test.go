package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

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
		if matchTerms(entryHaystack(e), terms) {
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

// The filter is a plain substring search spanning every column: one term finds
// the protocol, another the transport, another part of the name.
func TestFilter_SubstringAcrossColumns(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"vless", []string{"de1.example.ru", "de2.example.ru"}},
		{"ws", []string{"de1.example.ru", "ws-node.example.com", "nl1.example.ru"}},
		{"герман", []string{"de1.example.ru", "de2.example.ru"}},
		{"nl1", []string{"nl1.example.ru"}},
		{"vless ws", []string{"de1.example.ru"}},
		{"герман tcp", []string{"de2.example.ru"}},
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
		{"Германия", "Германия"},
		{"🇩🇪", "🇩🇪"},
	}
	for _, tt := range tests {
		if got := flagSpace(tt.in); got != tt.want {
			t.Errorf("flagSpace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// address:port identifies a server across a refresh, where names change and
// positions shift.
func TestIndexOfServer(t *testing.T) {
	entries := []subscription.SubEntry{
		{Address: "de1.example.ru", Port: 443},
		{Address: "nl1.example.ru", Port: 443},
		{Address: "nl1.example.ru", Port: 8443},
	}
	tests := []struct {
		address string
		port    int
		want    int
	}{
		{"nl1.example.ru", 443, 1},
		{"nl1.example.ru", 8443, 2},
		{"", 0, 0},
		{"old.example.ru", 443, 0},
	}
	for _, tt := range tests {
		if got := indexOfServer(entries, tt.address, tt.port); got != tt.want {
			t.Errorf("indexOfServer(%q, %d) = %d, want %d", tt.address, tt.port, got, tt.want)
		}
	}
}

// bestKey names the lowest successful latency; a failed measurement never wins.
func TestBestKey(t *testing.T) {
	c := PingCache{
		"a:1": {Latency: 300 * time.Millisecond},
		"b:1": {Latency: 50 * time.Millisecond},
		"c:1": {Error: errors.New("timeout")},
		"d:1": {Latency: 120 * time.Millisecond},
	}
	if got := bestKey(c); got != "b:1" {
		t.Errorf("bestKey = %q, want b:1 (the 50ms server)", got)
	}
	if got := bestKey(PingCache{"c:1": {Error: errors.New("x")}}); got != "" {
		t.Errorf("bestKey over failures only = %q, want empty", got)
	}
}
