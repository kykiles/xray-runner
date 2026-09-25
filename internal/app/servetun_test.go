package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xraycfg"
)

// The service's tun session goes through the session's own bring-up and
// teardown: the servers kept off the tunnel and let through the kill switch
// are read off the outbounds it runs, and everything comes down when the
// session's context ends.
func TestServeTun(t *testing.T) {
	binary := buildGenerationXray(t)
	t.Setenv("MOCK_XRAY_STARTS", t.TempDir()+"/starts")
	t.Setenv("MOCK_XRAY_CRASHES", "0")
	// The databases the interface handed over: the config test and every
	// core run on them, not on the service's own.
	geoDir := t.TempDir()
	writeGeoDat(t, filepath.Join(geoDir, "geoip.dat"), "private")
	writeGeoDat(t, filepath.Join(geoDir, "geosite.dat"), "ru-blocked")
	t.Setenv("MOCK_XRAY_ASSET", geoDir)

	var mu sync.Mutex
	var ks system.KillSwitchConfig
	var route system.TunRouteConfig
	var events []string
	note := func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	old := prepareServiceApp
	prepareServiceApp = func(a *App) {
		n := &tunNet{a: a, index: map[int]int{}, born: map[int]time.Time{}}
		a.interfaces = n.interfaces
		a.enableTunRouting = func(c system.TunRouteConfig) error {
			mu.Lock()
			route = c
			mu.Unlock()
			note("route")
			return n.enableTunRouting(c)
		}
		a.disableTunRouting = func() error { note("unroute"); return n.disableTunRouting() }
		a.enableKillSwitch = func(c system.KillSwitchConfig) error {
			mu.Lock()
			ks = c
			mu.Unlock()
			note("ks on")
			return nil
		}
		a.disableKillSwitch = func() error { note("ks off"); return nil }
		a.directBind = func() (xraycfg.DirectBind, error) { return xraycfg.DirectBind{Mark: 255}, nil }
		a.healthProbe = func() (bool, time.Duration) { return true, time.Millisecond }
	}
	t.Cleanup(func() { prepareServiceApp = old })

	spec := ServiceTun{
		Parts: xraycfg.ClientParts{Outbounds: json.RawMessage(`[
			{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.5","port":443,"users":[{"id":"u","encryption":"none"}]}]}},
			{"tag":"alt","protocol":"hysteria2","settings":{"address":"203.0.113.6","port":8443}},
			{"tag":"direct","protocol":"freedom"}]`)},
		LogLevel:   "warning",
		KillSwitch: true,
		CheckURLs:  []string{"http://127.0.0.1:1/"},
		Title:      "srv",
		Binary:     binary,
		GeoDir:     geoDir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	var sawHealth sync.Once
	health := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ServeTun(ctx, spec, func(err error) { ready <- err }, func(u tui.StatusUpdate) {
			if u.OK {
				sawHealth.Do(func() { close(health) })
			}
		})
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("not ready")
	}
	select {
	case <-health:
	case <-time.After(20 * time.Second):
		t.Fatal("no health update")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("ServeTun: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ks.Endpoints) != 2 || ks.Endpoints[0] != (system.Endpoint{IP: "203.0.113.5", Port: 443}) ||
		ks.Endpoints[1] != (system.Endpoint{IP: "203.0.113.6", Port: 8443, UDP: true}) || ks.Core != binary {
		t.Errorf("kill switch %+v", ks)
	}
	if len(route.ServerIPs) != 2 {
		t.Errorf("server exceptions %v", route.ServerIPs)
	}
	want := []string{"route", "ks on", "unroute", "ks off"}
	if len(events) < len(want) {
		t.Fatalf("events %v", events)
	}
	for i, e := range want {
		if events[i] != e {
			t.Fatalf("events %v, want %v first", events, want)
		}
	}
}

// A request the service must not run fails before anything is touched.
func TestServeTunRefusesBadParts(t *testing.T) {
	old := prepareServiceApp
	prepareServiceApp = func(a *App) {
		a.enableTunRouting = func(system.TunRouteConfig) error { t.Error("routes touched"); return nil }
		a.directBind = func() (xraycfg.DirectBind, error) { return xraycfg.DirectBind{}, nil }
	}
	t.Cleanup(func() { prepareServiceApp = old })
	var readyErr error
	err := ServeTun(context.Background(), ServiceTun{
		Parts:    xraycfg.ClientParts{Outbounds: json.RawMessage(`[{"protocol":"vless","streamSettings":{"tlsSettings":{"masterKeyLog":"/etc/x"}}}]`)},
		LogLevel: "warning",
	}, func(e error) { readyErr = e }, func(tui.StatusUpdate) {})
	if err == nil || readyErr == nil {
		t.Fatal("bad parts accepted")
	}
}

// writeGeoDat writes a geo database carrying the named lists.
func writeGeoDat(t *testing.T, path string, names ...string) {
	t.Helper()
	var out []byte
	for _, n := range names {
		entry := append([]byte{0x0A, byte(len(n))}, n...)
		out = append(out, 0x0A, byte(len(entry)))
		out = append(out, entry...)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A service session's config is checked against the databases its interface
// handed over: a list only they carry passes where the service's own would
// refuse it, and one they lack is refused with their folder named.
func TestFinalizeConfigInChecksTheGivenGeo(t *testing.T) {
	own := t.TempDir()
	writeGeoDat(t, filepath.Join(own, "geoip.dat"), "private")
	writeGeoDat(t, filepath.Join(own, "geosite.dat"), "google")
	xraycfg.SetGeoAssets(own, "")
	t.Cleanup(func() { xraycfg.SetGeoAssets("", "") })
	given := t.TempDir()
	writeGeoDat(t, filepath.Join(given, "geoip.dat"), "private")
	writeGeoDat(t, filepath.Join(given, "geosite.dat"), "ru-blocked")

	cfg := json.RawMessage(`{"routing":{"rules":[{"domain":["geosite:ru-blocked"],"outboundTag":"proxy"}]}}`)
	if _, err := finalizeConfig(cfg, false); err == nil {
		t.Fatal("the service's own databases carry ru-blocked: the test checks nothing")
	}
	if _, err := finalizeConfigIn(cfg, false, given); err != nil {
		t.Errorf("finalizeConfigIn = %v, want nil", err)
	}
	cfg = json.RawMessage(`{"routing":{"rules":[{"domain":["geosite:google"],"outboundTag":"proxy"}]}}`)
	if _, err := finalizeConfigIn(cfg, false, given); err == nil || !strings.Contains(err.Error(), given) {
		t.Errorf("finalizeConfigIn = %v, want google refused in %s", err, given)
	}
}
