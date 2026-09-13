package app

// A12: buildSessionConfig asked the machine itself how the direct path leaves
// the tunnel — on Windows a PowerShell lookup of the physical adapter — so
// every test of a TUN config depended on the adapters and CIM rights of the
// machine running it. The lookup is a seam now, like the routing calls.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xray-runner/internal/system"
	"xray-runner/internal/xraycfg"
)

// fakeDirectBind has the shape the platform's own lookup returns, without the
// lookup: a socket mark on Linux, an adapter name on Windows.
func fakeDirectBind() (xraycfg.DirectBind, error) {
	if runtime.GOOS == "windows" {
		return xraycfg.DirectBind{Interface: "Ethernet"}, nil
	}
	return xraycfg.DirectBind{Mark: xraycfg.DirectFwMark}, nil
}

func TestBuildSessionConfig_TunBindsDirectFromSeam(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	want, _ := fakeDirectBind()
	calls := 0
	a.directBind = func() (xraycfg.DirectBind, error) { calls++; return want, nil }

	raw, _, err := a.buildSessionConfig(&target{profileName: "Auto", profileRaw: json.RawMessage(panelProfile)})
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if calls != 1 {
		t.Errorf("directBind called %d times, want 1", calls)
	}

	var cfg struct {
		Outbounds []struct {
			Tag            string `json:"tag"`
			StreamSettings struct {
				Sockopt struct {
					Mark      int    `json:"mark"`
					Interface string `json:"interface"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	for _, ob := range cfg.Outbounds {
		if ob.Tag != "direct" {
			continue
		}
		got := xraycfg.DirectBind{Mark: ob.StreamSettings.Sockopt.Mark, Interface: ob.StreamSettings.Sockopt.Interface}
		if got != want {
			t.Errorf("direct outbound bound to %+v, want %+v", got, want)
		}
		return
	}
	t.Error("no direct outbound in the config")
}

// Without the direct path the TUN config would loop freedom traffic back into
// the tunnel, so a failed lookup ends the session before anything is started
// or changed on the machine.
func TestRunSession_DirectBindFailureStopsBeforeCore(t *testing.T) {
	isolateState(t)
	a := newTemplateApp(t)
	a.mode = "tun"
	a.cfg.KillSwitch = true
	a.tmpFile = filepath.Join(t.TempDir(), "xray_config.json")
	var routed, killSwitched int
	a.enableTunRouting = func(system.TunRouteConfig) error { routed++; return nil }
	a.enableKillSwitch = func(system.KillSwitchConfig) error { killSwitched++; return nil }
	a.directBind = func() (xraycfg.DirectBind, error) {
		return xraycfg.DirectBind{}, errors.New("adapter lookup failed")
	}

	_, err := a.runSession(context.Background(), coreSessionTarget())
	if err == nil || !strings.Contains(err.Error(), "adapter lookup failed") {
		t.Fatalf("runSession err = %v, want the lookup failure", err)
	}
	if a.runner != nil {
		t.Error("a core runner was created after the lookup failed")
	}
	if _, err := os.Stat(a.tmpFile); !os.IsNotExist(err) {
		t.Errorf("config written after the lookup failed (stat err = %v)", err)
	}
	if routed != 0 || killSwitched != 0 {
		t.Errorf("routes enabled %d times, kill switch %d times, want neither", routed, killSwitched)
	}
}
