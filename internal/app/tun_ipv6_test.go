package app

// A11: IPv6 used to be claimed only in split mode. Plain tun left it alone, and
// on a dual-stack network a v6-capable app went around the tunnel with its real
// address. The VPN itself runs without IPv6, so tun now claims it everywhere:
// the routing layer turns the claim into a block (Linux) or a capture into the
// tunnel (Windows), and the app falls back to IPv4 through the tunnel.

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/xraycfg"
)

// withIPv6Stack answers hasIPv6Stack for the test.
func withIPv6Stack(t *testing.T, present bool) {
	t.Helper()
	orig := hasIPv6Stack
	hasIPv6Stack = func() bool { return present }
	t.Cleanup(func() { hasIPv6Stack = orig })
}

func TestBringUpTun_ClaimsIPv6OutsideSplit(t *testing.T) {
	withIPv6Stack(t, true)
	var spy killSwitchSpy
	a := newKillSwitchApp(&spy, nil)
	a.cfg.KillSwitch = false
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	var got system.TunRouteConfig
	a.enableTunRouting = func(c system.TunRouteConfig) error { got = c; return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop() // the core's health loop goes with it
	if got.Addr6 != xraycfg.TunAddr6 {
		t.Errorf("Addr6 = %q outside split, want %q", got.Addr6, xraycfg.TunAddr6)
	}
}

func TestBuildSessionConfig_TunInboundHasIPv6Gateway(t *testing.T) {
	withIPv6Stack(t, true)
	a := newTemplateApp(t)
	a.mode = "tun"

	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if gw := tunGateway(t, raw); !slices.Contains(gw, xraycfg.TunAddr6+"/126") {
		t.Errorf("tun gateway = %v outside split, want the v6 address too", gw)
	}
}

// tunGateway is the addresses the config gives the tun interface.
func tunGateway(t *testing.T, raw []byte) []string {
	t.Helper()
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Gateway []string `json:"gateway"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tun" {
			return in.Settings.Gateway
		}
	}
	t.Fatal("no tun inbound")
	return nil
}

// A kernel booted with ipv6.disable=1 has no IPv6 to claim, and refuses the v6
// address: the core used to fail on it and the tun session never came up. The
// interface then takes IPv4 alone, and the routes claim nothing of IPv6.
func TestTun_WithoutIPv6Stack(t *testing.T) {
	withIPv6Stack(t, false)

	a := newTemplateApp(t)
	a.mode = "tun"
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if gw := tunGateway(t, raw); !slices.Equal(gw, []string{xraycfg.TunAddr + "/24"}) {
		t.Errorf("tun gateway = %v without an IPv6 stack, want IPv4 alone", gw)
	}

	var spy killSwitchSpy
	b := newKillSwitchApp(&spy, nil)
	b.cfg.KillSwitch = false
	b.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	var got system.TunRouteConfig
	b.enableTunRouting = func(c system.TunRouteConfig) error { got = c; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := b.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop()
	if got.Addr6 != "" {
		t.Errorf("Addr6 = %q without an IPv6 stack, want none", got.Addr6)
	}
}

// withV6Server answers tunAcceptsV6Server for the test.
func withV6Server(t *testing.T, accepts bool) {
	t.Helper()
	orig := tunAcceptsV6Server
	tunAcceptsV6Server = accepts
	t.Cleanup(func() { tunAcceptsV6Server = orig })
}

// v6OnlyTarget is a server with an IPv6 literal and no IPv4 address at all.
func v6OnlyTarget() *target {
	return &target{entry: &subscription.SubEntry{Protocol: "vless", Address: "2001:db8::5", Port: 443}}
}

// v6SetUpApp is a tun app whose routing calls are counted and recorded.
func v6SetUpApp(calls *int, got *system.TunRouteConfig) *App {
	var spy killSwitchSpy
	a := newKillSwitchApp(&spy, nil)
	a.cfg.KillSwitch = false
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	a.enableTunRouting = func(c system.TunRouteConfig) error { *calls++; *got = c; return nil }
	return a
}

// Where the routing layer can keep an IPv6 server outside the tunnel, a server
// with no IPv4 address is a valid one: the route set carries only the IPv6
// exception.
func TestTunSetUp_V6OnlyServerAcceptedWhereSupported(t *testing.T) {
	withIPv6Stack(t, true)
	withV6Server(t, true)
	var got system.TunRouteConfig
	calls := 0
	a := v6SetUpApp(&calls, &got)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := a.newTunLifecycle(v6OnlyTarget(), sessionPorts{}, false)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("tun setup for an IPv6-only server: %v", err)
	}
	defer l.afterStop()
	if calls != 1 {
		t.Fatalf("enableTunRouting called %d times, want 1", calls)
	}
	if len(got.ServerIPs) != 0 || !slices.Equal(got.ServerIPs6, []string{"2001:db8::5"}) {
		t.Errorf("route config servers = %v / %v, want none / [2001:db8::5]", got.ServerIPs, got.ServerIPs6)
	}
}

// Where the tunnel stays on IPv4 (Linux), or there is no IPv6 to claim, an
// IPv6-only server is refused as before and no route is touched: a tunnel
// without the server's exception would loop the uplink into itself.
func TestTunSetUp_V6OnlyServerRefusedOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name           string
		accepts, stack bool
	}{
		{"unsupported platform", false, true},
		{"no IPv6 stack", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withIPv6Stack(t, tc.stack)
			withV6Server(t, tc.accepts)
			var got system.TunRouteConfig
			calls := 0
			a := v6SetUpApp(&calls, &got)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l := a.newTunLifecycle(v6OnlyTarget(), sessionPorts{}, false)
			if err := l.afterStart(ctx); err == nil {
				l.afterStop()
				t.Fatal("tun setup accepted an IPv6-only server")
			}
			if calls != 0 {
				t.Errorf("enableTunRouting called %d times, want 0", calls)
			}
		})
	}
}

// The kill switch has to let the IPv6 uplink out too, where the tunnel carries
// IPv6 servers; elsewhere the list stays IPv4-only.
func TestKillSwitchEndpoints_V6OnlyWhereSupported(t *testing.T) {
	for _, tc := range []struct {
		name           string
		accepts, stack bool
		want           []system.Endpoint
	}{
		{"supported", true, true, []system.Endpoint{{IP: "2001:db8::5", Port: 443}}},
		{"unsupported", false, true, nil},
		{"no IPv6 stack", true, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withIPv6Stack(t, tc.stack)
			withV6Server(t, tc.accepts)
			a := &App{serverHost: "2001:db8::5", serverPort: 443}
			got := a.killSwitchEndpoints(v6OnlyTarget())
			if !slices.Equal(got, tc.want) {
				t.Errorf("endpoints = %+v, want %+v", got, tc.want)
			}
		})
	}
}
