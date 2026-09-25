package app

// A10: the TUN health check used a plain HTTP client, so the probe followed the
// system routes. With a route wrong, or the probe host sent `direct` by the
// profile, it went out the physical link and reported "ок" in front of a dead
// tunnel. It now goes through the session's own loopback inbound, the way the
// proxy-mode check goes through the proxy.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildSessionConfig_TunHasProbeInbound(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"

	raw, ports, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if ports.probe == 0 {
		t.Fatal("tun session has no probe port")
	}
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     int    `json:"port"`
			Listen   string `json:"listen"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "probe" {
			if in.Protocol != "http" || in.Listen != "127.0.0.1" || in.Port != ports.probe {
				t.Errorf("probe inbound = %+v, want http on 127.0.0.1:%d", in, ports.probe)
			}
			validateWithXray(t, raw)
			return
		}
	}
	t.Errorf("no probe inbound in %+v", cfg.Inbounds)
}

func TestTunProbeClient_UsesSessionInbound(t *testing.T) {
	client := newProbeClient(12345, time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatalf("probe client transport = %#v, want a proxying *http.Transport", client.Transport)
	}
	req, _ := http.NewRequest("GET", "https://www.google.com/generate_204", nil)
	u, err := tr.Proxy(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:12345" {
		t.Errorf("probe proxy = %v (%v), want 127.0.0.1:12345", u, err)
	}
}

// The internet answers directly, the inbound does not: the probe must fail.
func TestTunProbe_FailsWithoutInbound(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer direct.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	if _, err := timeRequest(context.Background(), newProbeClient(dead, 2*time.Second), direct.URL); err == nil {
		t.Error("probe passed with the inbound down — it went around the tunnel")
	}
}

// probeRules returns the rules aimed at the probe hosts of the default check
// URLs.
func probeRules(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	var out []map[string]any
	for _, r := range cfg.Routing.Rules {
		d, _ := r["domain"].([]any)
		if len(d) > 0 && d[0] == "full:www.google.com" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no probe rule in %v", cfg.Routing.Rules)
	}
	return out
}

// In tun the probe rules apply to the probe inbound only: on the tun inbound
// they would catch every program's connection to a probe host.
func TestBuildSessionConfig_TunProbeRuleOnProbeInbound(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	for _, r := range probeRules(t, raw) {
		in, _ := r["inboundTag"].([]any)
		if len(in) != 1 || in[0] != "probe" {
			t.Errorf("probe rule = %v, want inboundTag [probe]", r)
		}
	}
}

// In proxy mode the probe comes in through the http inbound like everything
// else, so its rule stays unlimited.
func TestBuildSessionConfig_ProxyProbeRuleUnlimited(t *testing.T) {
	a := newTemplateApp(t)
	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	for _, r := range probeRules(t, raw) {
		if r["inboundTag"] != nil {
			t.Errorf("probe rule = %v, want no inboundTag in proxy mode", r)
		}
	}
}
