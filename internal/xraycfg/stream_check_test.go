package xraycfg

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A18: the builder turned combinations the core cannot run into a config
// without a word — REALITY over WebSocket fails at xray start, and
// xtls-rprx-vision without RAW under TLS or REALITY never carries a byte.
func TestBuildOutbounds_RefuseIncompatibleStream(t *testing.T) {
	const pbk = "&sni=a.com&pbk=Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw&sid=ab"
	cases := []struct {
		name, scheme, query string
		ok                  bool
	}{
		{"reality over ws", "vless", "type=ws&security=reality" + pbk, false},
		{"reality over httpupgrade", "vless", "type=httpupgrade&security=reality" + pbk, false},
		{"vision over ws", "vless", "type=ws&flow=xtls-rprx-vision", false},
		{"vision without security", "vless", "type=tcp&flow=xtls-rprx-vision", false},
		{"trojan reality over ws", "trojan", "type=ws&security=reality" + pbk, false},
		{"reality over tcp with vision", "vless", "type=tcp&security=reality&flow=xtls-rprx-vision" + pbk, true},
		{"reality over grpc", "vless", "type=grpc&security=reality&serviceName=s" + pbk, true},
		{"reality over xhttp", "vless", "type=xhttp&security=reality" + pbk, true},
		{"vision over tls", "vless", "type=tcp&security=tls&flow=xtls-rprx-vision&sni=a.com", true},
		{"ws over tls", "vless", "type=ws&security=tls&sni=a.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &url.URL{Scheme: c.scheme, Host: "example.com:443", User: url.User("secret"), RawQuery: c.query}
			var err error
			if c.scheme == "trojan" {
				_, err = BuildTrojanOutbound(u)
			} else {
				_, err = BuildVLESSOutbound(u)
			}
			if c.ok && err != nil {
				t.Errorf("%s refused: %v", c.query, err)
			}
			if !c.ok && err == nil {
				t.Errorf("%s built without a word", c.query)
			}
		})
	}
}

// Golden fixtures pin the builder's output byte for byte, so they must be
// configs the core actually accepts — two of them used to be ones it refused
// or could not run.
func TestGoldenFixturesPassXrayTest(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("..", "..", "xray_linux", "xray"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("xray binary not present, skipping core validation")
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no golden fixtures found: %v", err)
	}
	for _, f := range fixtures {
		outbound, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cfg := filepath.Join(t.TempDir(), "cfg.json")
		if err := os.WriteFile(cfg, []byte(`{"outbounds":[`+string(outbound)+`]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "run", "-test", "-config", cfg).CombinedOutput(); err != nil {
			t.Errorf("%s: xray rejected it: %v\n%s", f, err, out)
		}
	}
}
