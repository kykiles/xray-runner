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
	buildFn func(*url.URL) interface{}
}

func TestBuildVLESSOutbound(t *testing.T) {
	tests := []testCase{
		{
			name:   "vless_ws_reality",
			rawURL: "vless://uuid@example.com:443?type=ws&security=reality&path=%2Fvless-ws&host=example.com&sni=example.com&pbk=publickey&sid=123456&fp=chrome",
			golden: "testdata/vless_ws_reality.json",
			buildFn: func(u *url.URL) interface{} { return BuildVLESSOutbound(u) },
		},
		{
			name:   "vless_grpc_tls",
			rawURL: "vless://uuid-grpc@grpc.example.com:443?type=grpc&security=tls&serviceName=myservice&authority=grpc.example.com&sni=grpc.example.com&fp=chrome&alpn=h2,http/1.1",
			golden: "testdata/vless_grpc_tls.json",
			buildFn: func(u *url.URL) interface{} { return BuildVLESSOutbound(u) },
		},
		{
			name:   "vless_ws_flow",
			rawURL: "vless://uuid-flow@flow.example.com:443?type=ws&path=%2Fflow&flow=xtls-rprx-vision",
			golden: "testdata/vless_ws_flow.json",
			buildFn: func(u *url.URL) interface{} { return BuildVLESSOutbound(u) },
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
			buildFn: func(u *url.URL) interface{} { return BuildSSOutbound(u) },
		},
	}

	runTests(t, tests)
}

func runTests(t *testing.T, tests []testCase) {
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}

			result := tc.buildFn(u)

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

			if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
				t.Errorf("mismatch for %s\n  got:  %s\n  want: %s", tc.name, string(got), string(want))
			}
		})
	}
}
