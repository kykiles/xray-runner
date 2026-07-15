package tui

import (
	"testing"

	"xray-runner/internal/subscription"
)

// A host spelling "ws" is what makes a plain substring filter useless for the
// transport: "t:ws" must not match it.
var filterFixture = []subscription.SubEntry{
	{Remarks: "🇩🇪 Германия 1", Address: "de1.example.ru", Protocol: "vless", Network: "ws"},
	{Remarks: "🇩🇪 Германия 2", Address: "de2.example.ru", Protocol: "vless", Network: "tcp"},
	{Remarks: "🇺🇸 США", Address: "ws-node.example.com", Protocol: "ss", Network: "tcp"},
	{Remarks: "🇳🇱 Нидерланды", Address: "nl1.example.ru", Protocol: "vmess", Network: "ws"},
}

func matching(t *testing.T, query string) []string {
	t.Helper()
	f := parseFilter(query)
	var out []string
	for _, e := range filterFixture {
		if f.match(e) {
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

func TestFilter_ProtocolAndTransport(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"p:vless", []string{"de1.example.ru", "de2.example.ru"}},
		{"t:ws", []string{"de1.example.ru", "nl1.example.ru"}},
		{"p:vless t:ws", []string{"de1.example.ru"}},
		// Free text still matches name or host...
		{"герман", []string{"de1.example.ru", "de2.example.ru"}},
		{"nl1", []string{"nl1.example.ru"}},
		// ...and combines with the scoped tokens.
		{"p:vless t:ws герман", []string{"de1.example.ru"}},
		// Unscoped "ws" searches name and host only — the ws transport of de1/nl1
		// is not part of that text, so only the host spelling "ws" matches.
		{"ws", []string{"ws-node.example.com"}},
		// "ss" is a prefix of no protocol but shadowsocks, so vless is excluded
		// even though the word "vless" contains "ss".
		{"p:ss", []string{"ws-node.example.com"}},
		// Contradictory tokens narrow to nothing rather than widening.
		{"p:vless p:ss", nil},
		{"p:vless герман t:tcp", []string{"de2.example.ru"}},
	}

	for _, tt := range tests {
		if got := matching(t, tt.query); !equal(got, tt.want) {
			t.Errorf("filter %q = %v, want %v", tt.query, got, tt.want)
		}
	}
}

// "t:ws" must never fall back to the host, or the prefix buys nothing over the
// old substring filter.
func TestFilter_ScopedTokenIgnoresHost(t *testing.T) {
	for _, addr := range matching(t, "t:ws") {
		if addr == "ws-node.example.com" {
			t.Error("t:ws matched a host spelling ws instead of the transport")
		}
	}
}

func TestFilter_EmptyAndCaseInsensitive(t *testing.T) {
	if got := matching(t, ""); len(got) != len(filterFixture) {
		t.Errorf("empty filter = %v, want everything", got)
	}
	if got := matching(t, "P:VLESS T:WS"); !equal(got, []string{"de1.example.ru"}) {
		t.Errorf("uppercase filter = %v", got)
	}
}
