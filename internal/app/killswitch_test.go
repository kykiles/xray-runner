package app

// A02: a kill switch that was asked for but did not come up must fail the
// session, not leave it "connected" with nothing guarding against a leak.

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
)

type killSwitchSpy struct {
	routed, unrouted, ksOff int
}

// newKillSwitchApp is a tun session whose interface is up and whose routing and
// firewall calls land in the spy instead of on the machine.
func newKillSwitchApp(spy *killSwitchSpy, enable func(system.KillSwitchConfig) error) *App {
	return &App{
		cfg:  &config.Config{Mode: "tun", KillSwitch: true},
		mode: "tun",
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "xray-tun", Flags: net.FlagUp}}, nil
		},
		enableTunRouting:  func(system.TunRouteConfig) error { spy.routed++; return nil },
		disableTunRouting: func() error { spy.unrouted++; return nil },
		enableKillSwitch:  enable,
		disableKillSwitch: func() error { spy.ksOff++; return nil },
		// A set-up core is checked; these tests do not look at the result.
		tunProbe: func() (bool, time.Duration) { return true, 0 },
	}
}

// A literal address, so neither the routes nor the whitelist wait on DNS.
func killSwitchTarget() *target {
	return &target{entry: &subscription.SubEntry{Protocol: "vless", Address: "203.0.113.5", Port: 443}}
}

func TestBringUpTun_KillSwitchFailureFailsSession(t *testing.T) {
	var spy killSwitchSpy
	a := newKillSwitchApp(&spy, func(system.KillSwitchConfig) error { return errors.New("iptables: permission denied") })
	a.serverHost, a.serverPort = "203.0.113.5", 443

	if err := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false).afterStart(context.Background()); err == nil {
		t.Fatal("tun setup succeeded with the kill switch down")
	}
	if a.killSwitchOn {
		t.Error("killSwitchOn set although EnableKillSwitch failed")
	}

	// The routes went in before the kill switch; the session's teardown takes
	// them back out.
	a.releaseSession()
	if spy.unrouted != 1 {
		t.Errorf("tun routes removed %d times, want 1", spy.unrouted)
	}
	if spy.ksOff != 0 {
		t.Errorf("kill switch disabled %d times, want 0 — it never came up", spy.ksOff)
	}
}

func TestBringUpTun_KillSwitchWithoutEndpointsFailsSession(t *testing.T) {
	var spy killSwitchSpy
	enabled := false
	a := newKillSwitchApp(&spy, func(system.KillSwitchConfig) error { enabled = true; return nil })
	// No endpoint recorded: the whitelist would be empty and cut xray's uplink.

	if err := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false).afterStart(context.Background()); err == nil {
		t.Fatal("tun setup succeeded with no server to whitelist")
	}
	if enabled {
		t.Error("EnableKillSwitch called with an empty whitelist")
	}
	if a.killSwitchOn {
		t.Error("killSwitchOn set without a kill switch")
	}
}

// Split over the tunnel (Windows) is set up as tun, minus the kill switch: it
// would cut everything the split sends past the tunnel on purpose (ADR-0003).
func TestBringUpTun_SplitSkipsKillSwitch(t *testing.T) {
	var spy killSwitchSpy
	enabled := false
	a := newKillSwitchApp(&spy, func(system.KillSwitchConfig) error { enabled = true; return nil })
	a.serverHost, a.serverPort = "203.0.113.5", 443

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, true)
	if err := l.afterStart(context.Background()); err != nil {
		t.Fatalf("split setup: %v", err)
	}
	defer l.afterStop() // the core's health loop goes with it
	if enabled || a.killSwitchOn {
		t.Error("kill switch enabled in split")
	}
	if spy.routed != 1 {
		t.Errorf("tun routes installed %d times, want 1", spy.routed)
	}
}

// Proxy mode has no kill switch to enable, which is not a failure — but the user
// who set KILL_SWITCH=true has to hear that it does nothing here. The note joins
// whatever bring-up left before it rather than replacing it.
func TestBringUp_ProxyKillSwitchLeavesNote(t *testing.T) {
	a := newTemplateApp(t)
	a.cfg.KillSwitch = true
	a.pendingNote = "Прошлый режим TUN недоступен"

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the ports never open; only the note matters here
	_ = a.bringUp(ctx, sessionPorts{socks: 1, http: 2})

	if !strings.Contains(a.pendingNote, "только в TUN") {
		t.Errorf("pendingNote = %q, want a kill switch note", a.pendingNote)
	}
	if !strings.Contains(a.pendingNote, "Прошлый режим TUN недоступен") {
		t.Errorf("pendingNote = %q, the earlier note was lost", a.pendingNote)
	}
}
