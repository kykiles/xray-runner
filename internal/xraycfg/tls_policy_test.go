package xraycfg

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
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

// insecureAt spells the four names on the policy's path, each optionally in
// another case, around a flag the opt-out has to remove.
func insecureAt(outbounds, stream, tls, flag string) string {
	return `{"` + outbounds + `": [{"tag": "proxy", "protocol": "trojan",
	  "` + stream + `": {"network": "tcp", "security": "tls",
	    "` + tls + `": {"serverName": "a.example", "` + flag + `": true, "futureKnob": "keep"}}}]}`
}

// F05: the core decodes JSON into Go structs, which match field names without
// regard to case, so "TlsSettings" and "AllowInsecure" reached it exactly as
// the lowercase spellings did — while a policy comparing exact keys walked past
// them and left ALLOW_INSECURE=false unenforced.
func TestApplySecurityPolicy_MixedCaseKeysAreEnforced(t *testing.T) {
	cases := map[string]string{
		"outbounds":      insecureAt("Outbounds", "streamSettings", "tlsSettings", "allowInsecure"),
		"streamSettings": insecureAt("outbounds", "StreamSettings", "tlsSettings", "allowInsecure"),
		"tlsSettings":    insecureAt("outbounds", "streamSettings", "TlsSettings", "allowInsecure"),
		"allowInsecure":  insecureAt("outbounds", "streamSettings", "tlsSettings", "AllowInsecure"),
		"all four":       insecureAt("Outbounds", "StreamSettings", "TLSSettings", "AllowInsecure"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// An unambiguous config must be normalized, never refused: making
			// these green by rejecting mixed case would break working profiles.
			got, err := ApplySecurityPolicy(json.RawMessage(raw), false)
			if err != nil {
				t.Fatalf("unambiguous mixed-case config must be accepted: %v", err)
			}
			var cfg struct {
				Outbounds []struct {
					StreamSettings struct {
						TLSSettings map[string]any
					}
				}
			}
			if err := json.Unmarshal(got, &cfg); err != nil {
				t.Fatalf("result is not JSON: %v", err)
			}
			if len(cfg.Outbounds) != 1 {
				t.Fatalf("outbounds lost: %s", got)
			}
			settings := cfg.Outbounds[0].StreamSettings.TLSSettings
			for k, v := range settings {
				if strings.EqualFold(k, "allowInsecure") {
					t.Errorf("case-insensitive key bypassed ALLOW_INSECURE=false: %s = %v", k, v)
				}
			}
			// Everything else the profile asked for survives untouched.
			if settings["serverName"] != "a.example" || settings["futureKnob"] != "keep" {
				t.Errorf("the policy changed more than the flag: %v", settings)
			}
		})
	}
}

// The opt-in keeps a mixed-case flag exactly as it keeps a lowercase one, and
// still adds nothing of its own.
func TestApplySecurityPolicy_MixedCaseOptInKeeps(t *testing.T) {
	raw := insecureAt("Outbounds", "StreamSettings", "TLSSettings", "AllowInsecure")
	got, err := ApplySecurityPolicy(json.RawMessage(raw), true)
	if err != nil {
		t.Fatalf("ApplySecurityPolicy: %v", err)
	}
	sameJSON(t, got, raw)
}

// Two spellings of one security key, or the same spelling twice, are two
// answers to the same question. A Go map keeps one of them and loses the order,
// so the honest answer is to refuse rather than to pick — under either setting.
func TestApplySecurityPolicy_RepeatedSecurityKeyIsRefused(t *testing.T) {
	const tlsBody = `{"serverName": "a.example", "allowInsecure": true}`
	cases := map[string]struct{ raw, names string }{
		"tlsSettings twice, mixed case": {
			`{"outbounds": [{"streamSettings": {"security": "tls",
			  "tlsSettings": ` + tlsBody + `, "TLSSettings": ` + tlsBody + `}}]}`, "tlsSettings"},
		"tlsSettings twice, other order": {
			`{"outbounds": [{"streamSettings": {"security": "tls",
			  "TLSSettings": ` + tlsBody + `, "tlsSettings": ` + tlsBody + `}}]}`, "tlsSettings"},
		"allowInsecure twice, mixed case": {
			`{"outbounds": [{"streamSettings": {"security": "tls",
			  "tlsSettings": {"allowInsecure": true, "AllowInsecure": false}}}]}`, "allowInsecure"},
		"allowInsecure twice, same spelling": {
			`{"outbounds": [{"streamSettings": {"security": "tls",
			  "tlsSettings": {"allowInsecure": true, "allowInsecure": false}}}]}`, "allowInsecure"},
		"streamSettings twice": {
			`{"outbounds": [{"streamSettings": {"security": "tls", "tlsSettings": ` + tlsBody + `},
			  "StreamSettings": {"security": "tls", "tlsSettings": ` + tlsBody + `}}]}`, "streamSettings"},
		"outbounds twice": {
			`{"outbounds": [{"streamSettings": {"security": "tls", "tlsSettings": ` + tlsBody + `}}],
			  "Outbounds": [{"streamSettings": {"security": "tls", "tlsSettings": ` + tlsBody + `}}]}`, "outbounds"},
	}
	for name, c := range cases {
		for _, allow := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				got, err := ApplySecurityPolicy(json.RawMessage(c.raw), allow)
				if err == nil {
					t.Fatalf("allow=%v: an ambiguous config passed: %s", allow, got)
				}
				// The message has to name the key the user must fix, not just
				// report that something was wrong somewhere.
				if !strings.Contains(err.Error(), c.names) {
					t.Errorf("allow=%v: error does not name %s: %v", allow, c.names, err)
				}
			})
		}
	}
}

// The policy's reach is that one path. A field of the same name belonging to
// something else stays where it is, whatever its case.
func TestApplySecurityPolicy_LookAlikeOutsideTLSSurvives(t *testing.T) {
	raw := `{"outbounds": [
	  {"tag": "odd", "protocol": "future", "settings": {"AllowInsecure": true}},
	  {"tag": "proxy", "protocol": "trojan",
	   "streamSettings": {"security": "tls", "TlsSettings": {"serverName": "a", "AllowInsecure": true}}}]}`

	got, err := ApplySecurityPolicy(json.RawMessage(raw), false)
	if err != nil {
		t.Fatalf("ApplySecurityPolicy: %v", err)
	}
	var cfg struct {
		Outbounds []map[string]json.RawMessage
	}
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if !strings.Contains(string(cfg.Outbounds[0]["settings"]), "AllowInsecure") {
		t.Errorf("a look-alike outside tlsSettings was removed: %s", cfg.Outbounds[0]["settings"])
	}

	// The TLS flag itself has to be gone whatever case it was written in, and
	// the rest of tlsSettings has to stay. Read through the decoder rather than
	// searched for as text: the marshalled spacing is not the contract.
	var shaped struct {
		Outbounds []struct {
			StreamSettings struct {
				TLSSettings map[string]any
			}
		}
	}
	if err := json.Unmarshal(got, &shaped); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	settings := shaped.Outbounds[1].StreamSettings.TLSSettings
	for k, v := range settings {
		if strings.EqualFold(k, "allowInsecure") {
			t.Errorf("the TLS flag survived as %s = %v", k, v)
		}
	}
	if settings["serverName"] != "a" {
		t.Errorf("tlsSettings lost serverName: %v", settings)
	}
}
