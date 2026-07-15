//go:build linux

package system

import (
	"errors"
	"strings"
	"testing"
)

// fakeIP models the `ip` binary: it answers `route get` with a canned line and
// records every mutating command, so the routing logic can be verified without
// root or touching the host's routing table.
type fakeIP struct {
	missing     bool
	routeGetOut string            // fallback answer for `ip route get`
	routeGets   map[string]string // per-destination answers
	routeGetErr error
	cmds        []string
}

func (f *fakeIP) lookPath(bin string) error {
	if f.missing {
		return errors.New("not found")
	}
	return nil
}

func (f *fakeIP) run(bin string, args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "route" && args[1] == "get" {
		if out, ok := f.routeGets[args[2]]; ok {
			return []byte(out), nil
		}
		return []byte(f.routeGetOut), f.routeGetErr
	}
	f.cmds = append(f.cmds, bin+" "+strings.Join(args, " "))
	return nil, nil
}

func (f *fakeIP) has(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	orig := ipCmd
	ipCmd = f
	t.Cleanup(func() { ipCmd = orig })
}

// wanRouteGet is what `ip route get <server>` prints for a server reachable
// through the physical gateway.
const wanRouteGet = "45.150.32.235 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "

var tunCfg = TunRouteConfig{Iface: "xray-tun", ServerIPs: []string{"45.150.32.235"}}

// Without these two routes the tun device exists but no traffic ever enters
// it — the bug this whole file addresses.
func TestEnableTunRouting_SendsDefaultTrafficIntoTun(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 0.0.0.0/1 dev xray-tun") {
		t.Errorf("missing lower split-default route, got: %v", f.cmds)
	}
	if !f.has("route add 128.0.0.0/1 dev xray-tun") {
		t.Errorf("missing upper split-default route, got: %v", f.cmds)
	}
}

// The tunnel's own packets to the VPS must keep using the physical path,
// otherwise xray's uplink routes into the tun it is serving and deadlocks.
func TestEnableTunRouting_ExcludesServerViaPhysicalGateway(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 45.150.32.235/32 via 192.168.31.1 dev wlp3s0") {
		t.Errorf("missing server exclusion route, got: %v", f.cmds)
	}
}

// A balancer profile rotates across all its servers, so every one of them must
// stay on the physical path — not just the first.
func TestEnableTunRouting_ExcludesEveryServerOfBalancerProfile(t *testing.T) {
	f := &fakeIP{routeGets: map[string]string{
		"45.150.32.235": wanRouteGet,
		"203.0.113.7":   "203.0.113.7 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 ",
	}}
	withFakeIP(t, f)

	cfg := TunRouteConfig{Iface: "xray-tun", ServerIPs: []string{"45.150.32.235", "203.0.113.7"}}
	if err := EnableTunRouting(cfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	for _, want := range []string{
		"route add 45.150.32.235/32 via 192.168.31.1 dev wlp3s0",
		"route add 203.0.113.7/32 via 192.168.31.1 dev wlp3s0",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}

	f.cmds = nil
	DisableTunRouting()
	for _, want := range []string{"route del 45.150.32.235/32", "route del 203.0.113.7/32"} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A server on the same L2 segment has no gateway; the route must still pin the
// device, without a bogus `via`.
func TestEnableTunRouting_ExcludesServerOnLocalLink(t *testing.T) {
	f := &fakeIP{routeGetOut: "192.168.31.5 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "}
	withFakeIP(t, f)

	if err := EnableTunRouting(TunRouteConfig{Iface: "xray-tun", ServerIPs: []string{"192.168.31.5"}}); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 192.168.31.5/32 dev wlp3s0") {
		t.Errorf("missing on-link server exclusion, got: %v", f.cmds)
	}
	if f.has("via") {
		t.Errorf("on-link route must not carry a gateway, got: %v", f.cmds)
	}
}

// Installing the split default before knowing the server's physical path would
// blackhole the tunnel's own uplink, so this must fail before touching routes.
func TestEnableTunRouting_FailsWithoutTouchingRoutesWhenServerPathUnknown(t *testing.T) {
	f := &fakeIP{routeGetErr: errors.New("network is unreachable")}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the server's physical route can't be resolved")
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	DisableTunRouting()

	for _, want := range []string{
		"route del 0.0.0.0/1 dev xray-tun",
		"route del 128.0.0.0/1 dev xray-tun",
		"route del 45.150.32.235/32",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not fire stray `route del` at the host.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := &fakeIP{}
	withFakeIP(t, f)

	DisableTunRouting()

	if len(f.cmds) != 0 {
		t.Errorf("expected no commands, got: %v", f.cmds)
	}
}

func TestParseRouteGet(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		via     string
		dev     string
		wantErr bool
	}{
		{name: "via gateway", out: wanRouteGet, via: "192.168.31.1", dev: "wlp3s0"},
		{name: "on-link", out: "192.168.31.5 dev wlp3s0 src 192.168.31.94 ", via: "", dev: "wlp3s0"},
		{name: "no dev", out: "45.150.32.235 via 192.168.31.1 ", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			via, dev, err := parseRouteGet(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteGet: %v", err)
			}
			if via != tt.via || dev != tt.dev {
				t.Errorf("got via=%q dev=%q, want via=%q dev=%q", via, dev, tt.via, tt.dev)
			}
		})
	}
}
