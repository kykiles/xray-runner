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

func TestBringUpTun_ClaimsIPv6OutsideSplit(t *testing.T) {
	var spy killSwitchSpy
	a := newKillSwitchApp(&spy, nil)
	a.cfg.KillSwitch = false
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	var got system.TunRouteConfig
	a.enableTunRouting = func(c system.TunRouteConfig) error { got = c; return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.bringUpTun(ctx, killSwitchTarget()); err != nil {
		t.Fatalf("bringUpTun: %v", err)
	}
	if got.Addr6 != xraycfg.TunAddr6 {
		t.Errorf("Addr6 = %q outside split, want %q", got.Addr6, xraycfg.TunAddr6)
	}
}

func TestBuildSessionConfig_TunInboundHasIPv6Gateway(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"

	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
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
			if !slices.Contains(in.Settings.Gateway, xraycfg.TunAddr6+"/126") {
				t.Errorf("tun gateway = %v outside split, want the v6 address too", in.Settings.Gateway)
			}
			return
		}
	}
	t.Error("no tun inbound")
}
