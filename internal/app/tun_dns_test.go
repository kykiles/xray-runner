package app

// In tun mode the system's DNS went past the tunnel to the router on the LAN,
// and the provider saw every name the machine looked up. A tun session now
// hands the system's DNS to the tunnel, and the core gets its servers'
// addresses up front so it never looks them up through that same tunnel.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/xraycfg"
)

type dnsSpy struct {
	on, off int
	err     error
}

func withDNSSpy(t *testing.T) *dnsSpy {
	t.Helper()
	if !system.TunSwitchesDNS {
		t.Skip("this platform's tun session leaves the system's DNS alone")
	}
	spy := &dnsSpy{}
	origOn, origOff := enableTunDNS, disableTunDNS
	enableTunDNS = func(iface, _ string) error {
		if iface != xraycfg.TunInterfaceName {
			t.Errorf("DNS switched on %q", iface)
		}
		spy.on++
		return spy.err
	}
	disableTunDNS = func(string) error { spy.off++; return nil }
	t.Cleanup(func() { enableTunDNS, disableTunDNS = origOn, origOff })
	return spy
}

func dnsTunApp() *App {
	var ks killSwitchSpy
	a := newKillSwitchApp(&ks, nil)
	a.cfg.KillSwitch = false
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	// A second core checks the physical adapter has not changed under it.
	a.directBind = fakeDirectBind
	a.directBound, _ = fakeDirectBind()
	return a
}

// Every core brings up a fresh interface, and the DNS setting went with the
// old one: each core's setup switches it again, and the session's release
// hands the link back.
func TestTunDNS_SwitchedForEveryCoreAndHandedBack(t *testing.T) {
	spy := withDNSSpy(t)
	a := dnsTunApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	for range 2 {
		if err := l.afterStart(ctx); err != nil {
			t.Fatalf("tun setup: %v", err)
		}
		if err := l.afterStop(); err != nil {
			t.Fatalf("afterStop: %v", err)
		}
	}
	if spy.on != 2 {
		t.Errorf("DNS switched %d times over two cores, want 2", spy.on)
	}
	a.releaseSession()
	if spy.off != 1 || a.tunDNSOn {
		t.Errorf("release: handed back %d times, tunDNSOn=%v", spy.off, a.tunDNSOn)
	}
	a.releaseSession()
	if spy.off != 1 {
		t.Errorf("a second release handed the link back again")
	}
}

// A refusal leaves DNS on the old path: the tunnel still comes up, and the
// screen says once that names may go past it.
func TestTunDNS_FailureIsANoteNotAFailure(t *testing.T) {
	spy := withDNSSpy(t)
	spy.err = errors.New("systemd-resolved не запущен")
	a := dnsTunApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	for range 2 {
		if err := l.afterStart(ctx); err != nil {
			t.Fatalf("tun setup failed over DNS: %v", err)
		}
		_ = l.afterStop()
	}
	if !strings.Contains(a.pendingNote, "DNS системы не переведён в туннель: systemd-resolved не запущен") {
		t.Errorf("pendingNote = %q", a.pendingNote)
	}
	if strings.Count(a.pendingNote, "DNS системы") != 1 {
		t.Errorf("the note repeats for every core: %q", a.pendingNote)
	}
}

// The split sends only chosen processes through the tunnel; the system's DNS
// stays where it was.
func TestTunDNS_SplitLeavesDNSAlone(t *testing.T) {
	spy := withDNSSpy(t)
	a := dnsTunApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, true)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop()
	if spy.on != 0 {
		t.Errorf("split switched the system's DNS")
	}
}

// The core dials only the addresses pinned into its config, so the routes past
// the tunnel and the kill switch must hold exactly those, whatever a later
// lookup answers.
func TestTunDNS_PinnedServersReachRoutesAndKillSwitch(t *testing.T) {
	withDNSSpy(t)
	withIPv6Stack(t, false)
	origLookup := lookupServer
	lookupServer = func(h string) []string {
		if h == "b.invalid" {
			return []string{"198.51.100.4"}
		}
		return nil
	}
	t.Cleanup(func() { lookupServer = origLookup })

	a := newTemplateApp(t)
	a.mode = "tun"
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	var cfg struct {
		DNS struct {
			Hosts map[string][]string `json:"hosts"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DNS.Hosts["full:b.invalid"]; !slices.Equal(got, []string{"198.51.100.4"}) {
		t.Fatalf("dns.hosts[full:b.invalid] = %v", got)
	}

	// b.invalid resolves to nothing on the network: everything the routes and
	// the kill switch name comes from the pin.
	b := dnsTunApp()
	b.cfg.KillSwitch = true
	b.pinned = a.pinned
	var ks system.KillSwitchConfig
	b.enableKillSwitch = func(c system.KillSwitchConfig) error { ks = c; return nil }
	var routes system.TunRouteConfig
	b.enableTunRouting = func(c system.TunRouteConfig) error { routes = c; return nil }
	tgt := &target{entry: &subscription.SubEntry{Protocol: "vless", Address: "b.invalid", Port: 443}}
	b.serverHost, b.serverPort = "b.invalid", 443

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := b.newTunLifecycle(tgt, sessionPorts{}, false)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop()
	if !slices.Equal(routes.ServerIPs, []string{"198.51.100.4"}) {
		t.Errorf("route exceptions = %v, want the pinned address", routes.ServerIPs)
	}
	if len(ks.Endpoints) != 1 || ks.Endpoints[0].IP != "198.51.100.4" {
		t.Errorf("kill switch endpoints = %v, want the pinned address", ks.Endpoints)
	}
}

// hasTunDNSHijack reports whether a session config sends the tunnel's DNS to
// the panel's servers.
func hasTunDNSHijack(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var cfg struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Rules []struct {
					Action string `json:"action"`
				} `json:"rules"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, outbound := range cfg.Outbounds {
		if outbound.Tag == xraycfg.TunDNSOutboundTag {
			return len(outbound.Settings.Rules) > 0 && outbound.Settings.Rules[0].Action == "hijack"
		}
	}
	return false
}

// A tun session over a panel profile answers the system's DNS from the panel's
// dns servers; the proxy mode has no tunnel to take it from, and a config with
// no dns servers of its own — the template — would ask the system it answers.
func TestTunDNS_PanelServersAnswerTheTunnel(t *testing.T) {
	withDNSSpy(t)
	profile := &target{profileName: "Auto", profileRaw: json.RawMessage(panelProfile)}
	cases := []struct {
		name, mode string
		tgt        *target
		want       bool
	}{
		{"tun/panel profile", "tun", profile, true},
		{"proxy/panel profile", "proxy", profile, false},
		{"tun/template", "tun", splitTarget(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTemplateApp(t)
			a.mode = c.mode
			raw, _, err := a.buildSessionConfig(c.tgt)
			if err != nil {
				t.Fatalf("buildSessionConfig: %v", err)
			}
			if got := hasTunDNSHijack(t, raw); got != c.want {
				t.Errorf("hijacked = %v, want %v", got, c.want)
			}
		})
	}
}

// The split leaves the system's DNS out of the tunnel, so there is none to
// take: its config stays as built.
func TestTunDNS_SplitIsNotHijacked(t *testing.T) {
	a := newTemplateApp(t)
	a.split = true
	raw := json.RawMessage(`{"dns":{"servers":["https://8.8.8.8/dns-query"]},"outbounds":[{"tag":"proxy","protocol":"freedom"}]}`)
	if out := a.hijackDNS(raw); string(out) != string(raw) {
		t.Errorf("split config changed:\n%s", out)
	}
}

// ksDNSApp is a tun session with the kill switch asked for, whose switch and
// DNS calls land in order in *calls.
func ksDNSApp(t *testing.T, calls *[]string, got *system.KillSwitchConfig) *App {
	t.Helper()
	a := dnsTunApp()
	a.cfg.KillSwitch = true
	a.serverHost, a.serverPort = "203.0.113.5", 443
	a.enableKillSwitch = func(c system.KillSwitchConfig) error {
		*calls = append(*calls, "killswitch")
		*got = c
		return nil
	}
	return a
}

// With the system's DNS in the tunnel the kill switch shuts port 53 past it:
// the one lookup that would still go there is the system falling back to the
// router while the tunnel is down between two cores, and the provider would
// see the names. The DNS is switched first, so the switch knows.
func TestTunDNS_KillSwitchShutsDNSOnceItIsInTheTunnel(t *testing.T) {
	spy := withDNSSpy(t)
	var calls []string
	var ks system.KillSwitchConfig
	a := ksDNSApp(t, &calls, &ks)
	orig := enableTunDNS
	enableTunDNS = func(iface, core string) error {
		calls = append(calls, "dns")
		return orig(iface, core)
	}

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	if err := l.afterStart(context.Background()); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop()
	if !slices.Equal(calls, []string{"dns", "killswitch"}) {
		t.Errorf("calls %v, want the DNS switched before the kill switch", calls)
	}
	if ks.PassDNS {
		t.Error("kill switch lets DNS past the tunnel although the system's DNS is in it")
	}
	if spy.on != 1 {
		t.Errorf("DNS switched %d times", spy.on)
	}
}

// A system DNS that stayed on the physical path keeps port 53 open: shut, no
// name would resolve at all. The note already says the names may leak.
func TestTunDNS_KillSwitchPassesDNSWhenItStayedOut(t *testing.T) {
	spy := withDNSSpy(t)
	spy.err = errors.New("systemd-resolved не запущен")
	var calls []string
	var ks system.KillSwitchConfig
	a := ksDNSApp(t, &calls, &ks)

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	if err := l.afterStart(context.Background()); err != nil {
		t.Fatalf("tun setup: %v", err)
	}
	defer l.afterStop()
	if !ks.PassDNS {
		t.Error("kill switch shut DNS that only the physical path can carry")
	}
	if !strings.Contains(a.pendingNote, "могут уходить мимо VPN") {
		t.Errorf("pendingNote = %q", a.pendingNote)
	}
}

// A later core whose DNS switch fails under a kill switch that shut port 53:
// the names do not leak, they stop resolving — the note says that instead.
func TestTunDNS_LaterFailureUnderShutDNSSaysNamesStop(t *testing.T) {
	spy := withDNSSpy(t)
	var calls []string
	var ks system.KillSwitchConfig
	a := ksDNSApp(t, &calls, &ks)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l := a.newTunLifecycle(killSwitchTarget(), sessionPorts{}, false)
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("first core: %v", err)
	}
	_ = l.afterStop()
	spy.err = errors.New("systemd-resolved не запущен")
	if err := l.afterStart(ctx); err != nil {
		t.Fatalf("second core: %v", err)
	}
	defer l.afterStop()
	if strings.Count(strings.Join(calls, ","), "killswitch") != 1 || ks.PassDNS {
		t.Errorf("kill switch calls %v, PassDNS=%v: want one, with DNS shut", calls, ks.PassDNS)
	}
	if !strings.Contains(a.pendingNote, "сайты по именам не откроются") || strings.Contains(a.pendingNote, "могут уходить") {
		t.Errorf("pendingNote = %q", a.pendingNote)
	}
}
