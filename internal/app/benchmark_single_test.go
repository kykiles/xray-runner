package app

import (
	"encoding/json"
	"testing"

	"xray-runner/internal/subscription"
)

// The ping of a panel server must be built from the panel config, exactly as the
// session is (ADR-0002): the panel's dns resolves the server's domain, and the
// probe is aimed at the tested outbound instead of wherever the panel's own
// rules would send it.
func TestBuildSingleBenchConfigMatchesTheSession(t *testing.T) {
	pb := &ProxyBenchmarker{probeHosts: []string{"full:www.google.com"}}
	entry := subscription.SubEntry{
		Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		RawOutbound: outboundNamed(t, "proxy-2"),
		ProfileRaw:  json.RawMessage(panelProfile),
	}
	ports := portPair{socks: 41080, http: 41081}

	raw, ok := pb.buildSingleBenchConfig(entry, benchInbounds(ports))
	if !ok {
		t.Fatal("buildSingleBenchConfig() = false, want the panel config to be used")
	}

	var got struct {
		Inbounds []struct {
			Port int `json:"port"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"routing"`
		DNS json.RawMessage `json:"dns"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}

	if len(got.DNS) == 0 {
		t.Error("panel dns dropped — the server's domain would be resolved by the blocked network resolver")
	}
	if got.Outbounds[0].Tag != "proxy-2" {
		t.Errorf("outbounds[0] = %q, want the measured proxy-2", got.Outbounds[0].Tag)
	}
	if got.Routing.Rules[0]["domain"] == nil {
		t.Errorf("probe rule is not first: %v", got.Routing.Rules[0])
	}
	if len(got.Inbounds) == 0 || got.Inbounds[0].Port != ports.socks {
		t.Errorf("inbounds = %v, want the measuring ports", got.Inbounds)
	}
	validateWithXray(t, raw)
}

// A bare link or a URL-list subscription has no panel config: the caller must
// fall back to template.json rather than measure nothing.
func TestBuildSingleBenchConfigSkipsEntriesWithoutPanelConfig(t *testing.T) {
	pb := &ProxyBenchmarker{}
	entry := subscription.SubEntry{Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443}
	if _, ok := pb.buildSingleBenchConfig(entry, benchInbounds(portPair{})); ok {
		t.Error("buildSingleBenchConfig() = true, want false without a panel config")
	}
}
