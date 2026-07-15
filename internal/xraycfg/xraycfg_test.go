package xraycfg

import (
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
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
			name:   "vless_ws_reality",
			rawURL: "vless://uuid@example.com:443?type=ws&security=reality&path=%2Fvless-ws&host=example.com&sni=example.com&pbk=publickey&sid=123456&fp=chrome",
			golden: "testdata/vless_ws_reality.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
		{
			name:   "vless_grpc_tls",
			rawURL: "vless://uuid-grpc@grpc.example.com:443?type=grpc&security=tls&serviceName=myservice&authority=grpc.example.com&sni=grpc.example.com&fp=chrome&alpn=h2,http/1.1",
			golden: "testdata/vless_grpc_tls.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
		{
			name:   "vless_ws_flow",
			rawURL: "vless://uuid-flow@flow.example.com:443?type=ws&path=%2Fflow&flow=xtls-rprx-vision",
			golden: "testdata/vless_ws_flow.json",
			buildFn: func(u *url.URL) (interface{}, error) { return BuildVLESSOutbound(u) },
		},
	}

	runTests(t, tests)
}

func TestBuildSSOutbound(t *testing.T) {
	tests := []testCase{
		{
			name:   "ss_basic",
			rawURL: "ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8443",
			golden: "testdata/ss_basic.json",
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
