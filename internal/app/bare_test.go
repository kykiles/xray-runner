package app

import (
	"strings"
	"testing"
)

func TestValidateSubscriptionInput_Accepts(t *testing.T) {
	ok := []string{
		"https://sub.example.com/token",
		"http://sub.example.com/token",
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080?type=ws#srv",
		"ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8443#myserver",
		"hysteria2://secret@hy.example.com:443#hy",
		"trojan://pass@example.com:443?security=tls&sni=a.example.com#tr",
	}
	for _, s := range ok {
		if err := validateSubscriptionInput(s); err != nil {
			t.Errorf("validateSubscriptionInput(%q) = %v, want nil", s, err)
		}
	}
}

// The subscription list renders every entry through maskURL, so a bare link
// must not put its UUID (or ss password) on screen.
func TestMaskURL_BareLinkHidesCredentials(t *testing.T) {
	const uuid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	got := maskURL("vless://"+uuid+"@example.com:8080?type=ws#srv", true)
	if strings.Contains(got, uuid) {
		t.Errorf("maskURL leaked the UUID: %q", got)
	}
	if !strings.Contains(got, "example.com") {
		t.Errorf("maskURL = %q, want the host to stay visible", got)
	}

	const pass = "YWVzLTI1Ni1nY206c2VjcmV0"
	if got := maskURL("ss://"+pass+"@ss.example.com:8443#srv", true); strings.Contains(got, pass) {
		t.Errorf("maskURL leaked the ss credentials: %q", got)
	}
}

func TestValidateSubscriptionInput_Rejects(t *testing.T) {
	bad := []struct {
		in   string
		want string
	}{
		// A supported scheme with a missing UUID must not reach xray.
		{"vless://@example.com:8080?type=ws", "ссылка неполная"},
		{"vless://not-a-uuid@example.com:8080", "ссылка неполная"},
		// Port out of range.
		{"ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:99999", "ссылка неполная"},
		// Unsupported protocol and plain junk.
		{"ftp://example.com", "vless/vmess/ss/trojan/hysteria2"},
		{"just some text", "vless/vmess/ss/trojan/hysteria2"},
	}
	for _, c := range bad {
		err := validateSubscriptionInput(c.in)
		if err == nil {
			t.Errorf("validateSubscriptionInput(%q) = nil, want error", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("validateSubscriptionInput(%q) = %q, want it to mention %q", c.in, err, c.want)
		}
	}
}
