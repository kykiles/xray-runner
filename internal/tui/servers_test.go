package tui

import (
	"strings"
	"testing"

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
