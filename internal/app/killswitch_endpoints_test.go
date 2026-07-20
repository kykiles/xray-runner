package app

import (
	"testing"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
)

// The session runs under the panel's routing, which may send some domains
// through a sibling outbound of the same profile. Those siblings are xray's own
// uplink just as much as the chosen server is, so the kill switch has to let
// all of them out — whitelisting only the picked one blackholes the rest.
func TestKillSwitchEndpoints_CoversProfileSiblings(t *testing.T) {
	a := &App{serverHost: "203.0.113.5", serverPort: 443}
	tgt := &target{
		entry: &subscription.SubEntry{Address: "203.0.113.5", Port: 443},
		profileSrvs: []subscription.SubEntry{
			{Protocol: "vless", Address: "203.0.113.5", Port: 443},
			{Protocol: "vless", Address: "203.0.113.6", Port: 8443},
			{Protocol: "hysteria2", Address: "203.0.113.7", Port: 9443},
		},
	}

	got := a.killSwitchEndpoints(tgt)

	want := []system.Endpoint{
		{IP: "203.0.113.5", Port: 443},
		{IP: "203.0.113.6", Port: 8443},
		{IP: "203.0.113.7", Port: 9443, UDP: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d endpoints, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("endpoint %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// A server that appears both as the chosen one and inside the profile must not
// produce two identical rules.
func TestKillSwitchEndpoints_Deduplicates(t *testing.T) {
	a := &App{serverHost: "203.0.113.5", serverPort: 443}
	tgt := &target{
		entry:       &subscription.SubEntry{Address: "203.0.113.5", Port: 443},
		profileSrvs: []subscription.SubEntry{{Protocol: "vless", Address: "203.0.113.5", Port: 443}},
	}

	if got := a.killSwitchEndpoints(tgt); len(got) != 1 {
		t.Errorf("got %d endpoints, want 1: %+v", len(got), got)
	}
}
