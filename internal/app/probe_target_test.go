package app

// F04: the connectivity probe has to travel the outbound under test even when
// the check URL names an IP instead of a domain. A "full:" matcher compares the
// name a request carries, and a request to an IP URL carries none, so the probe
// rule never fired and the check fell through to the profile's own direct rule
// — reporting "ок" for a tunnel nothing was going through.
//
// Nothing here touches the machine: loopback sockets only, no system routes and
// no proxy settings.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"xray-runner/internal/xraycfg"
)

func TestProbeHostsSeparatesIPsFromDomains(t *testing.T) {
	got := probeHosts([]string{
		"https://www.google.com/generate_204",
		"http://127.0.0.1:8080/",
		"http://[::1]:8080/",
		"http://[2001:DB8::1]/",
	})
	// The domain keeps its exact-match prefix; the addresses come back as bare
	// literals in canonical form, ready for an ip rule.
	want := []string{"full:www.google.com", "127.0.0.1", "::1", "2001:db8::1"}
	if !slices.Equal(got, want) {
		t.Errorf("probeHosts() = %q, want %q", got, want)
	}
}

// A zone identifier survives probeHosts so that the rule builder can refuse it
// out loud, instead of it quietly becoming a matcher that matches nothing.
func TestProbeHostsKeepsAZoneForTheRuleBuilderToRefuse(t *testing.T) {
	got := probeHosts([]string{"http://[fe80::1%25eth0]:80/"})
	if !slices.Equal(got, []string{"fe80::1%eth0"}) {
		t.Fatalf("probeHosts() = %q, want the zoned literal kept", got)
	}
	if _, err := xraycfg.PrependProbeRule(json.RawMessage(`{"outbounds":[{"tag":"proxy"}]}`), got); err == nil {
		t.Error("a zoned address was turned into a rule instead of being refused")
	}
}

// probeCore is the real core, or the reason this check cannot run here.
func probeCore(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(repoRoot(t), "xray_linux", "xray")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("xray binary not present, skipping the live probe routing check")
	}
	return bin
}

// loopbackServer answers 204 on the given loopback address, or skips when the
// host has no such stack — an absent IPv6 loopback is a missing check, never a
// reason to fall back to a domain and call it covered.
func loopbackServer(t *testing.T, host string) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("no loopback listener on %s: %v", host, err)
	}
	srv := &httptest.Server{
		Listener: l,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})},
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// runProbeCore starts the core on a config whose first outbound is proto, with
// a direct rule covering directNet, and probes endpoint through it.
func runProbeCore(t *testing.T, core, proto, directNet, endpoint string) error {
	t.Helper()
	port := closedPort(t)
	raw := fmt.Appendf(nil, `{
		"log": {"loglevel": "none"},
		"inbounds": [{"tag": "probe", "listen": "127.0.0.1", "port": %d, "protocol": "http", "settings": {}}],
		"outbounds": [{"tag": "proxy", "protocol": %q}, {"tag": "direct", "protocol": "freedom"}],
		"routing": {"rules": [
			{"type": "field", "ip": [%q], "outboundTag": "direct"},
			{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"}
		]}
	}`, port, proto, directNet)

	raw, err := xraycfg.PrependProbeRule(raw, probeHosts([]string{endpoint}))
	if err != nil {
		t.Fatalf("PrependProbeRule: %v", err)
	}
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, raw, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, core, "run", "-c", cfg)
	cmd.Dir = repoRoot(t) // geoip.dat/geosite.dat live there
	if err := cmd.Start(); err != nil {
		t.Fatalf("start core: %v", err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	if !awaitPort(ctx, port, "", 5*time.Second) {
		t.Fatal("core inbound did not start")
	}

	client := newProbeClient(port, 2*time.Second)
	defer client.CloseIdleConnections()
	_, err = timeRequest(ctx, client, endpoint)
	return err
}

func TestProbeToLiteralIPTakesTheTestedOutbound(t *testing.T) {
	core := probeCore(t)
	for _, fam := range []struct{ name, host, directNet string }{
		{"ipv4", "127.0.0.1", "127.0.0.0/8"},
		{"ipv6", "::1", "::1/128"},
	} {
		t.Run(fam.name, func(t *testing.T) {
			endpoint := loopbackServer(t, fam.host).URL

			// The outbound under test drops everything, and direct would reach
			// the endpoint happily. A probe that succeeds here went around the
			// outbound it was supposed to be reporting on.
			if err := runProbeCore(t, core, "blackhole", fam.directNet, endpoint); err == nil {
				t.Error("probe succeeded through direct although the tested outbound blocks every request")
			}

			// Positive control: with a working outbound the same probe must get
			// its 204. Without it, refusing every probe would pass for a fix.
			if err := runProbeCore(t, core, "freedom", fam.directNet, endpoint); err != nil {
				t.Errorf("probe failed through a working outbound: %v", err)
			}
		})
	}
}
