package subscription

import (
	"strings"
	"testing"
)

// Remarks and profile names come straight from the panel and end up on the
// terminal and in the log file. A hostile subscription must not be able to ship
// escape sequences that repaint the screen or forge log lines.
func TestParse_StripsControlCharactersFromRemarks(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{
			name: "ansi clear screen in vless fragment",
			link: "vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp#%1b%5b2JEvil",
			want: "[2JEvil",
		},
		{
			name: "newline in fragment forges a log line",
			link: "vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp#good%0Afake",
			want: "goodfake",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := parseURL(tt.link)
			if err != nil {
				t.Fatalf("parseURL: %v", err)
			}
			got := e.Remarks
			if strings.ContainsAny(got, "\x1b\n\r") {
				t.Errorf("Remarks still carries control characters: %q", got)
			}
			if got != tt.want {
				t.Errorf("Remarks = %q, want %q", got, tt.want)
			}
		})
	}
}
