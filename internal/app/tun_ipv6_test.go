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
