package app

import (
	"encoding/json"
	"testing"

	"xray-runner/internal/subscription"
)

// The point of measuring a profile is that the provider's balancing rules
// decide the path — so the balancer, routing and outbounds must survive intact,
// with only the inbounds swapped for our loopback ports.
func TestBuildProfileBenchConfigKeepsBalancerAndRouting(t *testing.T) {
	raw := json.RawMessage(`{
		"inbounds": [{"tag": "socks", "port": 10808, "protocol": "socks"}],
		"outbounds": [
			{"tag": "proxy-1", "protocol": "vless"},
			{"tag": "proxy-2", "protocol": "vless"}
		],
		"routing": {
			"balancers": [{"tag": "balancer", "selector": ["proxy"], "strategy": {"type": "leastPing"}}],
			"rules": [{"type": "field", "network": "tcp,udp", "balancerTag": "balancer"}]
		},
		"remarks": "Balanced"
	}`)
	p := subscription.Profile{Name: "Balanced", Raw: raw}
	ports := portPair{socks: 41080, http: 41081}

	out, err := buildProfileBenchConfig(p, ports, "error", nil)
	if err != nil {
		t.Fatalf("buildProfileBenchConfig() = %v, want nil", err)
	}

	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     int    `json:"port"`
			Listen   string `json:"listen"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Balancers []struct {
				Tag string `json:"tag"`
			} `json:"balancers"`
			Rules []struct {
				BalancerTag string `json:"balancerTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("unmarshal merged config: %v", err)
	}

	if len(cfg.Routing.Balancers) != 1 || cfg.Routing.Balancers[0].Tag != "balancer" {
		t.Errorf("balancers = %+v, want the profile's own balancer preserved", cfg.Routing.Balancers)
	}
	if len(cfg.Routing.Rules) != 1 || cfg.Routing.Rules[0].BalancerTag != "balancer" {
		t.Errorf("rules = %+v, want the profile's balancer rule preserved", cfg.Routing.Rules)
	}
	if len(cfg.Outbounds) != 2 {
		t.Errorf("outbounds = %d, want both of the profile's outbounds", len(cfg.Outbounds))
	}

	// The profile's own inbounds are replaced: their ports may be taken by the
	// running session, and only the outbound path is under test.
	if len(cfg.Inbounds) != 2 {
		t.Fatalf("inbounds = %d, want 2 bench inbounds", len(cfg.Inbounds))
	}
	byTag := map[string]int{}
	for _, in := range cfg.Inbounds {
		byTag[in.Tag] = in.Port
		if in.Listen != "127.0.0.1" {
			t.Errorf("inbound %q listens on %q, want 127.0.0.1", in.Tag, in.Listen)
		}
	}
	if byTag["socks"] != ports.socks || byTag["http"] != ports.http {
		t.Errorf("bench ports = %+v, want socks=%d http=%d", byTag, ports.socks, ports.http)
	}
}

// A profile that carries no panel config is one server; it must not be pushed
// through the profile-merge path, which has no config to merge.
func TestMeasureProfileWithoutRawRejectsEmptyProfile(t *testing.T) {
	pb := NewProxyBenchmarker(nil, "", benchTestConfig(0))
	r := pb.measureProfile(t.Context(), subscription.Profile{}, portPair{}, t.TempDir())
	if r.Error == nil {
		t.Fatal("measureProfile() error = nil, want an error for a profile with no servers")
	}
}
