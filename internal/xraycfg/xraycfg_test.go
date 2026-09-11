package xraycfg

import (
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

type testCase struct {
	name    string
	rawURL  string
	golden  string
	buildFn func(*url.URL) (interface{}, error)
}

func TestBuildVLESSOutbound(t *testing.T) {
	tests := []testCase{
		{
			name:    "vless_ws_tls",
			rawURL:  "vless://uuid@example.com:443?type=ws&security=tls&path=%2Fvless-ws&host=example.com&sni=example.com&fp=chrome",
			golden:  "testdata/vless_ws_tls.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
		{
			name:    "vless_grpc_tls",
			rawURL:  "vless://uuid-grpc@grpc.example.com:443?type=grpc&security=tls&serviceName=myservice&authority=grpc.example.com&sni=grpc.example.com&fp=chrome&alpn=h2,http/1.1",
			golden:  "testdata/vless_grpc_tls.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
		{
			name:    "vless_tcp_reality_vision",
			rawURL:  "vless://uuid-flow@flow.example.com:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=flow.example.com&pbk=Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw&sid=123456&fp=chrome",
			golden:  "testdata/vless_tcp_reality_vision.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
	}

	runTests(t, tests)
}

func TestBuildSSOutbound(t *testing.T) {
	tests := []testCase{
		{
			name:    "ss_basic",
			rawURL:  "ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8443",
			golden:  "testdata/ss_basic.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildSSOutbound(u) },
		},
	}

	runTests(t, tests)
}

func TestBuildVLESSOutboundErrors(t *testing.T) {
	t.Run("no_port", func(t *testing.T) {
		u, err := url.Parse("vless://uuid@example.com")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		if _, err := BuildVLESSOutbound(u); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("ipv6", func(t *testing.T) {
		u, err := url.Parse("vless://uuid@[2001:db8::1]:443")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		if _, err := BuildVLESSOutbound(u); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("non_numeric_port", func(t *testing.T) {
		u := &url.URL{Scheme: "vless", Host: "example.com:abc", User: url.User("uuid")}
		if _, err := BuildVLESSOutbound(u); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// TestBuildOutbounds_RejectOutOfRangePort covers M-3: strconv.Atoi happily
// accepts 0 and 99999, so a broken link used to surface as an opaque xray
// runtime error instead of a parse failure.
func TestBuildOutbounds_RejectOutOfRangePort(t *testing.T) {
	ports := []string{"0", "99999", "-1"}
	for _, p := range ports {
		t.Run("vless_"+p, func(t *testing.T) {
			u := &url.URL{Scheme: "vless", Host: "example.com:" + p, User: url.User("uuid")}
			if _, err := BuildVLESSOutbound(u); err == nil {
				t.Fatalf("port %s: expected error, got nil", p)
			}
		})
		t.Run("trojan_"+p, func(t *testing.T) {
			u := &url.URL{Scheme: "trojan", Host: "example.com:" + p, User: url.User("secret")}
			if _, err := BuildTrojanOutbound(u); err == nil {
				t.Fatalf("port %s: expected error, got nil", p)
			}
		})
		t.Run("ss_"+p, func(t *testing.T) {
			u := &url.URL{Scheme: "ss", Host: "example.com:" + p, User: url.User("YWVzLTI1Ni1nY206c2VjcmV0")}
			if _, err := BuildSSOutbound(u); err == nil {
				t.Fatalf("port %s: expected error, got nil", p)
			}
		})
	}
}

// TestSecuritySettings_AllowInsecure covers M-1: the caller's opt-in has to
// reach TLSSettings. Reality has no X.509 verification to switch off, so the
// flag must never appear there.
func TestSecuritySettings_AllowInsecure(t *testing.T) {
	t.Run("tls_honors_flag", func(t *testing.T) {
		u, err := url.Parse("vless://uuid@example.com:443?security=tls&sni=a.com&allowInsecure=1")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		ob, err := BuildVLESSOutbound(u)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if ob.Stream.TLSSettings == nil || !ob.Stream.TLSSettings.AllowInsecure {
			t.Fatal("expected AllowInsecure to be set")
		}
	})

	t.Run("tls_defaults_off", func(t *testing.T) {
		u, err := url.Parse("vless://uuid@example.com:443?security=tls&sni=a.com")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		ob, err := BuildVLESSOutbound(u)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if ob.Stream.TLSSettings.AllowInsecure {
			t.Fatal("AllowInsecure must stay off without the flag")
		}
	})

	t.Run("trojan_honors_flag", func(t *testing.T) {
		u, err := url.Parse("trojan://secret@example.com:443?security=tls&allowInsecure=true")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		ob, err := BuildTrojanOutbound(u)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if ob.Stream.TLSSettings == nil || !ob.Stream.TLSSettings.AllowInsecure {
			t.Fatal("expected AllowInsecure to be set")
		}
	})

	t.Run("reality_ignores_flag", func(t *testing.T) {
		u, err := url.Parse("vless://uuid@example.com:443?security=reality&pbk=k&allowInsecure=1")
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		ob, err := BuildVLESSOutbound(u)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if ob.Stream.TLSSettings != nil {
			t.Fatal("reality must not produce tlsSettings")
		}
	})
}

func TestBuildSSOutboundErrors(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
	}{
		{"no_port", "ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com"},
		{"ipv6", "ss://YWVzLTI1Ni1nY206c2VjcmV0@[2001:db8::1]:8443"},
		{"invalid_base64", "ss://!!!invalid-base64!!!@ss.example.com:8443"},
		{"bad_userinfo_format", "ss://dGVzdA==@ss.example.com:8443"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}
			if _, err := BuildSSOutbound(u); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestTransportSettings covers L-4: every transport branch of
// setTransportSettings, shared by vless and trojan, was untested.
func TestTransportSettings(t *testing.T) {
	cases := []struct {
		name  string
		query string
		check func(*testing.T, *StreamSettings)
	}{
		{"ws", "type=ws&path=/p&host=h.example", func(t *testing.T, ss *StreamSettings) {
			if ss.Network != "ws" || ss.WSSettings == nil {
				t.Fatalf("no ws settings: %+v", ss)
			}
			if ss.WSSettings.Path != "/p" || ss.WSSettings.Headers.Host != "h.example" {
				t.Errorf("ws settings = %+v", ss.WSSettings)
			}
		}},
		{"grpc_multi", "type=grpc&serviceName=svc&mode=multi&authority=a.example", func(t *testing.T, ss *StreamSettings) {
			if ss.GRPCSettings == nil {
				t.Fatal("no grpc settings")
			}
			if ss.GRPCSettings.ServiceName != "svc" || !ss.GRPCSettings.MultiMode || ss.GRPCSettings.Authority != "a.example" {
				t.Errorf("grpc settings = %+v", ss.GRPCSettings)
			}
		}},
		{"splithttp_normalizes_to_xhttp", "type=splithttp&path=/x&host=h.example&mode=stream-one", func(t *testing.T, ss *StreamSettings) {
			if ss.Network != "xhttp" {
				t.Errorf("network = %q, want xhttp", ss.Network)
			}
			if ss.XHTTPSettings == nil || ss.XHTTPSettings.Mode != "stream-one" || ss.XHTTPSettings.Path != "/x" {
				t.Errorf("xhttp settings = %+v", ss.XHTTPSettings)
			}
		}},
		{"httpupgrade", "type=httpupgrade&path=/hu&host=h.example", func(t *testing.T, ss *StreamSettings) {
			if ss.HTTPUpgradeSettings == nil || ss.HTTPUpgradeSettings.Path != "/hu" {
				t.Errorf("httpupgrade settings = %+v", ss.HTTPUpgradeSettings)
			}
		}},
		{"no_type_defaults_to_tcp", "", func(t *testing.T, ss *StreamSettings) {
			if ss.Network != "tcp" {
				t.Errorf("network = %q, want tcp", ss.Network)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse("vless://uuid@example.com:443?" + c.query)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}
			ob, err := BuildVLESSOutbound(u)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			c.check(t, ob.Stream)
		})
	}
}

// TestBuildTrojanOutbound covers L-4: trojan had no test of its own even though
// it shares the transport/security helpers with vless.
func TestBuildTrojanOutbound(t *testing.T) {
	u, err := url.Parse("trojan://s3cret@tr.example.com:8443?type=ws&path=/t&security=tls&sni=sni.example&alpn=h2,http/1.1")
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	ob, err := BuildTrojanOutbound(u)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if ob.Tag != "proxy" || ob.Protocol != "trojan" {
		t.Errorf("tag/protocol = %s/%s", ob.Tag, ob.Protocol)
	}
	srv := ob.Settings.Servers[0]
	if srv.Address != "tr.example.com" || srv.Port != 8443 || srv.Password != "s3cret" {
		t.Errorf("server = %+v", srv)
	}
	if ob.Stream.WSSettings == nil || ob.Stream.WSSettings.Path != "/t" {
		t.Errorf("ws settings = %+v", ob.Stream.WSSettings)
	}
	if ob.Stream.TLSSettings.ServerName != "sni.example" {
		t.Errorf("sni = %q", ob.Stream.TLSSettings.ServerName)
	}
	if !reflect.DeepEqual(ob.Stream.TLSSettings.ALPN, []string{"h2", "http/1.1"}) {
		t.Errorf("alpn = %v", ob.Stream.TLSSettings.ALPN)
	}
}

// TestAddCatchAllRule covers L-4: the rule that sends everything unmatched to
// the proxy must land last, after the template's own rules.
func TestAddCatchAllRule(t *testing.T) {
	rulesOf := func(t *testing.T, raw json.RawMessage) []map[string]interface{} {
		t.Helper()
		var r struct {
			Rules []map[string]interface{} `json:"rules"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal routing: %v", err)
		}
		return r.Rules
	}

	t.Run("appends_after_existing", func(t *testing.T) {
		rules := rulesOf(t, AddCatchAllRule(json.RawMessage(`{"rules":[{"outboundTag":"direct","domain":["geosite:ru"]}]}`)))
		if len(rules) != 2 {
			t.Fatalf("rules = %d, want 2", len(rules))
		}
		if rules[0]["outboundTag"] != "direct" {
			t.Errorf("existing rule moved: %+v", rules[0])
		}
		if rules[1]["outboundTag"] != "proxy" || rules[1]["network"] != "tcp,udp" {
			t.Errorf("catch-all = %+v", rules[1])
		}
	})

	t.Run("nil_routing", func(t *testing.T) {
		if rules := rulesOf(t, AddCatchAllRule(nil)); len(rules) != 1 {
			t.Fatalf("rules = %d, want 1", len(rules))
		}
	})

	t.Run("malformed_degrades_to_catch_all", func(t *testing.T) {
		if rules := rulesOf(t, AddCatchAllRule(json.RawMessage(`{broken`))); len(rules) != 1 {
			t.Fatalf("rules = %d, want 1", len(rules))
		}
	})
}

// TestMergeConfig covers L-4: the proxy outbound has to come first, since xray
// treats the first outbound as the default for unmatched traffic.
func TestMergeConfig(t *testing.T) {
	tc := &XrayConfig{
		Inbounds:  []Inbound{{Tag: "socks", Port: 1080, Protocol: "socks"}},
		Outbounds: []json.RawMessage{json.RawMessage(`{"tag":"direct","protocol":"freedom"}`)},
		Routing:   json.RawMessage(`{"rules":[]}`),
		Log:       &LogConfig{Loglevel: "debug"},
	}

	got := MergeConfig(tc, json.RawMessage(`{"tag":"proxy","protocol":"vless"}`))

	if len(got.Outbounds) != 2 {
		t.Fatalf("outbounds = %d, want 2", len(got.Outbounds))
	}
	if OutboundTag(got.Outbounds[0]) != "proxy" {
		t.Errorf("first outbound = %q, want proxy", OutboundTag(got.Outbounds[0]))
	}
	if OutboundTag(got.Outbounds[1]) != "direct" {
		t.Errorf("second outbound = %q, want direct", OutboundTag(got.Outbounds[1]))
	}
	// The caller owns the log level, so MergeConfig must not carry one over.
	if got.Log != nil {
		t.Errorf("log = %+v, want nil", got.Log)
	}
	if len(got.Inbounds) != 1 || got.Inbounds[0].Tag != "socks" {
		t.Errorf("inbounds = %+v", got.Inbounds)
	}
}

func TestLoadTemplateValidation(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "template.json")
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatalf("write template: %v", err)
		}
		return p
	}

	t.Run("valid", func(t *testing.T) {
		p := write(t, `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}],"routing":{"rules":[{"outboundTag":"direct"},{"outboundTag":"proxy"}]}}`)
		if _, err := LoadTemplate(p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("broken routing json", func(t *testing.T) {
		p := write(t, `{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":"not-an-array"}}`)
		if _, err := LoadTemplate(p); err == nil {
			t.Fatal("expected error for malformed routing")
		}
	})

	t.Run("dangling outboundTag", func(t *testing.T) {
		p := write(t, `{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[{"outboundTag":"nope"}]}}`)
		if _, err := LoadTemplate(p); err == nil {
			t.Fatal("expected error for unknown outboundTag")
		}
	})
}

func runTests(t *testing.T, tests []testCase) {
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}

			result, err := tc.buildFn(u)
			if err != nil {
				t.Fatalf("build: %v", err)
			}

			got, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			goldenPath := filepath.Join(".", tc.golden)
			if *update {
				if err := os.WriteFile(goldenPath, got, 0644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to create)", tc.golden, err)
			}

			gotStr := strings.ReplaceAll(strings.TrimSpace(string(got)), "\r\n", "\n")
			wantStr := strings.ReplaceAll(strings.TrimSpace(string(want)), "\r\n", "\n")
			if gotStr != wantStr {
				t.Errorf("mismatch for %s\n  got:  %s\n  want: %s", tc.name, string(got), string(want))
			}
		})
	}
}

// The tun inbound names its interface via "name"; xray ignores any other key
// and falls back to xray0, which awaitTUNInterface would then wait for forever.
func TestBuildTUNInboundInterfaceName(t *testing.T) {
	var settings struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(BuildTUNInbound().Settings, &settings); err != nil {
		t.Fatalf("unmarshal tun settings: %v", err)
	}
	if settings.Name != TunInterfaceName {
		t.Errorf("tun settings name = %q, want %q", settings.Name, TunInterfaceName)
	}
}

// TestBuildTUNInboundSettings pins the whole settings block so L-2 (building it
// from the TUNSettings struct instead of a format string) cannot drift.
func TestBuildTUNInboundSettings(t *testing.T) {
	var got TUNSettings
	if err := json.Unmarshal(BuildTUNInbound().Settings, &got); err != nil {
		t.Fatalf("unmarshal tun settings: %v", err)
	}
	want := TUNSettings{
		MTU:           1500,
		Gateway:       []string{TunAddr + "/24", TunAddr6 + "/126"},
		InterfaceName: TunInterfaceName,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tun settings = %+v, want %+v", got, want)
	}
}

// Tun claims IPv6 too (split and plain alike), which the interface can only do
// with a v6 address of its own.
func TestBuildTUNInboundIPv6(t *testing.T) {
	var got TUNSettings
	if err := json.Unmarshal(BuildTUNInbound().Settings, &got); err != nil {
		t.Fatalf("unmarshal tun settings: %v", err)
	}
	want := []string{TunAddr + "/24", TunAddr6 + "/126"}
	if !reflect.DeepEqual(got.Gateway, want) {
		t.Errorf("tun gateway = %v, want %v", got.Gateway, want)
	}
}
