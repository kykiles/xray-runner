package xraycfg

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// A01: a panel profile runs close to verbatim, so its own
// tlsSettings.allowInsecure reached the core past ALLOW_INSECURE=false. The
// flag sits on the second and third outbounds (a dialerProxy chain), never on
// the first, next to everything the policy must leave alone.
const policyProfile = `{
  "dns": {"servers": ["1.1.1.1"]},
  "futureTop": {"keep": true},
  "outbounds": [
    {"tag": "proxy", "protocol": "vless",
     "streamSettings": {"network": "tcp", "security": "reality",
       "realitySettings": {"serverName": "r.example", "publicKey": "PUB", "shortId": "ab"}}},
    {"tag": "proxy-2", "protocol": "trojan",
     "streamSettings": {"network": "ws", "security": "tls", "wsSettings": {"path": "/ws"},
       "tlsSettings": {"serverName": "b.example", "alpn": ["h2"], "fingerprint": "chrome",
         "pinnedPeerCertSha256": "ab:cd", "certificates": [{"usage": "verify", "certificate": ["CA"]}],
         "allowInsecure": true, "futureKnob": "keep"},
       "sockopt": {"dialerProxy": "hop"}}},
    {"tag": "hop", "protocol": "trojan",
     "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "c.example", "allowInsecure": true}}},
    {"tag": "plain-tls", "protocol": "trojan",
     "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "d.example", "allowInsecure": false}}},
    {"tag": "odd", "protocol": "future", "settings": {"allowInsecure": true}},
    {"tag": "direct", "protocol": "freedom"}
  ],
  "routing": {"balancers": [{"tag": "B", "selector": ["proxy"]}],
    "rules": [{"type": "field", "network": "tcp,udp", "balancerTag": "B"}]}
}`

// policyProfileCleared is policyProfile with the three tlsSettings flags gone
// and nothing else touched — the look-alike in "odd" is not a TLS setting.
const policyProfileCleared = `{
  "dns": {"servers": ["1.1.1.1"]},
  "futureTop": {"keep": true},
  "outbounds": [
    {"tag": "proxy", "protocol": "vless",
     "streamSettings": {"network": "tcp", "security": "reality",
       "realitySettings": {"serverName": "r.example", "publicKey": "PUB", "shortId": "ab"}}},
    {"tag": "proxy-2", "protocol": "trojan",
     "streamSettings": {"network": "ws", "security": "tls", "wsSettings": {"path": "/ws"},
       "tlsSettings": {"serverName": "b.example", "alpn": ["h2"], "fingerprint": "chrome",
         "pinnedPeerCertSha256": "ab:cd", "certificates": [{"usage": "verify", "certificate": ["CA"]}],
         "futureKnob": "keep"},
       "sockopt": {"dialerProxy": "hop"}}},
    {"tag": "hop", "protocol": "trojan",
     "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "c.example"}}},
    {"tag": "plain-tls", "protocol": "trojan",
     "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "d.example"}}},
    {"tag": "odd", "protocol": "future", "settings": {"allowInsecure": true}},
    {"tag": "direct", "protocol": "freedom"}
  ],
  "routing": {"balancers": [{"tag": "B", "selector": ["proxy"]}],
    "rules": [{"type": "field", "network": "tcp,udp", "balancerTag": "B"}]}
}`

func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("config differs:\n got %s\nwant %s", got, want)
	}
}

func TestApplySecurityPolicy_OptOutClearsEveryOutbound(t *testing.T) {
	got, err := ApplySecurityPolicy(json.RawMessage(policyProfile), false)
	if err != nil {
		t.Fatalf("ApplySecurityPolicy: %v", err)
	}
	sameJSON(t, got, policyProfileCleared)
}

// The opt-in keeps what the profile asked for and asks for nothing on its own:
// a TLS outbound without the flag stays verified.
func TestApplySecurityPolicy_OptInKeepsButNeverAdds(t *testing.T) {
	got, err := ApplySecurityPolicy(json.RawMessage(policyProfile), true)
	if err != nil {
		t.Fatalf("ApplySecurityPolicy: %v", err)
	}
	sameJSON(t, got, policyProfile)
}

// Anything but a JSON boolean is a value the core may read either way; guessing
// could leave verification off, so the config is refused under both settings.
func TestApplySecurityPolicy_NonBooleanIsRefused(t *testing.T) {
	for _, v := range []string{`"true"`, `"false"`, `1`, `null`, `{}`} {
		raw := `{"outbounds": [{"tag": "proxy", "streamSettings": {"security": "tls", "tlsSettings": {"allowInsecure": ` + v + `}}}]}`
		for _, allow := range []bool{false, true} {
			got, err := ApplySecurityPolicy(json.RawMessage(raw), allow)
			if err == nil {
				t.Errorf("allowInsecure=%s, allow=%v: got %s, want an error", v, allow, got)
			}
		}
	}
}

func TestApplySecurityPolicy_BrokenConfigIsRefused(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`{"outbounds": {}}`,
		`{"outbounds": [{"streamSettings": "tls"}]}`,
		`{"outbounds": [{"streamSettings": {"tlsSettings": []}}]}`,
	} {
		if got, err := ApplySecurityPolicy(json.RawMessage(raw), false); err == nil || got != nil {
			t.Errorf("%s: got %s, %v; want nil and an error", raw, got, err)
		}
	}
}

// Nothing to clear means nothing rewritten: the bytes come back as they went in.
func TestApplySecurityPolicy_NothingToClear(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"outbounds": [{"tag": "direct", "protocol": "freedom"}, {"tag": "x", "streamSettings": null}]}`,
		`{"outbounds": [{"tag": "t", "streamSettings": {"security": "tls", "tlsSettings": {"serverName": "a"}}}]}`,
	} {
		got, err := ApplySecurityPolicy(json.RawMessage(raw), false)
		if err != nil || !bytes.Equal(got, []byte(raw)) {
			t.Errorf("%s: got %s, %v; want it unchanged", raw, got, err)
		}
	}
}

// The profile's bytes are cached and shown in the preview; the policy works on
// a copy.
func TestApplySecurityPolicy_SourceUntouched(t *testing.T) {
	src := []byte(policyProfile)
	keep := bytes.Clone(src)
	if _, err := ApplySecurityPolicy(src, false); err != nil {
		t.Fatalf("ApplySecurityPolicy: %v", err)
	}
	if !bytes.Equal(src, keep) {
		t.Error("the source config was modified in place")
	}
}
