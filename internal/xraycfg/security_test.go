package xraycfg

import (
	"net/url"
	"testing"
)

// A07: only the exact strings "tls" and "reality" used to switch protection on.
// security=Reality built a bare outbound — no reality block, pbk/sid/sni thrown
// away, the UUID sent in the clear — and nothing said so. Case and spaces are
// now normalized, an empty value (or "none") stays legal, and anything else is
// refused instead of silently dropping protection.
func TestSecuritySettings_Normalized(t *testing.T) {
	cases := []struct {
		security string
		want     string // "" = no security block; "error" = refused
	}{
		{"reality", "reality"},
		{"Reality", "reality"},
		{"REALITY", "reality"},
		{" reality ", "reality"},
		{"tls", "tls"},
		{"TLS", "tls"},
		{"", ""},
		{"none", ""},
		{"xtls", "error"},
		{"tls1.3", "error"},
		{"foo", "error"},
	}
	builders := map[string]func(*url.URL) (*StreamSettings, error){
		"vless": func(u *url.URL) (*StreamSettings, error) {
			ob, err := BuildVLESSOutbound(u)
			if err != nil {
				return nil, err
			}
			return ob.Stream, nil
		},
		"trojan": func(u *url.URL) (*StreamSettings, error) {
			ob, err := BuildTrojanOutbound(u)
			if err != nil {
				return nil, err
			}
			return ob.Stream, nil
		},
	}

	for scheme, build := range builders {
		for _, c := range cases {
			t.Run(scheme+"_"+c.security, func(t *testing.T) {
				q := url.Values{}
				q.Set("security", c.security)
				q.Set("sni", "a.com")
				q.Set("pbk", "PUBKEY")
				q.Set("sid", "ab12")
				u := &url.URL{Scheme: scheme, Host: "example.com:443", User: url.User("secret"), RawQuery: q.Encode()}

				ss, err := build(u)
				if c.want == "error" {
					if err == nil {
						t.Fatalf("security=%q built an outbound (security=%q), want an error", c.security, ss.Security)
					}
					return
				}
				if err != nil {
					t.Fatalf("security=%q: %v", c.security, err)
				}
				if ss.Security != c.want {
					t.Errorf("security=%q → %q, want %q", c.security, ss.Security, c.want)
				}
				switch c.want {
				case "reality":
					r := ss.Reality
					if r == nil || r.PublicKey != "PUBKEY" || r.ShortID != "ab12" || r.ServerName != "a.com" {
						t.Errorf("security=%q: realitySettings = %+v, want pbk/sid/sni kept", c.security, r)
					}
				case "tls":
					if ss.TLSSettings == nil {
						t.Errorf("security=%q: no tlsSettings", c.security)
					}
				case "":
					if ss.Reality != nil || ss.TLSSettings != nil {
						t.Errorf("security=%q: got a security block, want none", c.security)
					}
				}
			})
		}
	}
}
