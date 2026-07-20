package subscription

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
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

// Every string that comes off a subscription must be stripped of control runes,
// not only the ones rendered today: the next screen that prints an SNI would
// otherwise reopen the terminal-injection hole.
func TestParseURL_SanitizesTransportFields(t *testing.T) {
	e, err := parseURL("vless://b831381d-6324-4d53-ad4f-8cda48b30811@example.com:443" +
		"?type=ws&sni=a%1b[2Jb&host=c%0ad&path=%2Fe%1bf&serviceName=g%07h#node")
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	for name, got := range map[string]string{
		"SNI": e.SNI, "Host": e.Host, "Path": e.Path, "ServiceName": e.ServiceName,
	} {
		if strings.IndexFunc(got, unicode.IsControl) >= 0 {
			t.Errorf("%s = %q, still carries a control rune", name, got)
		}
	}
}

// The same applies to servers extracted from a full Xray config, which reach
// applyStream through the real JSON path.
func TestOutboundToEntry_SanitizesTransportFields(t *testing.T) {
	raw := `{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"example.com","port":443,
		"users":[{"id":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}]},
		"streamSettings":{"network":"ws","security":"tls",
		"tlsSettings":{"serverName":"a\u001b[2Jb","fingerprint":"chr\u0007ome"},
		"wsSettings":{"path":"/p\u001bath","headers":{"Host":"h\nost"}}}}`

	var o xrayOutbound
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	e, ok := outboundToEntry(&o)
	if !ok {
		t.Fatal("outboundToEntry rejected a valid vless outbound")
	}

	for name, got := range map[string]string{
		"SNI": e.SNI, "Fingerprint": e.Fingerprint, "Path": e.Path, "Host": e.Host,
	} {
		if strings.IndexFunc(got, unicode.IsControl) >= 0 {
			t.Errorf("%s = %q, still carries a control rune", name, got)
		}
	}
}
