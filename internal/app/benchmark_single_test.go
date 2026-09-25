package app

import (
	"context"
	"encoding/json"
	"strings"
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

	raw, ok, err := pb.buildSingleBenchConfig(entry, benchInbounds(ports))
	if err != nil || !ok {
		t.Fatalf("buildSingleBenchConfig() = %v, %v, want the panel config to be used", ok, err)
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
	if _, ok, err := pb.buildSingleBenchConfig(entry, benchInbounds(portPair{})); ok || err != nil {
		t.Errorf("buildSingleBenchConfig() = %v, %v, want false and no error without a panel config", ok, err)
	}
}

// E03: a panel profile that is there but cannot carry the server is an error,
// not a ping measured on template.json instead.
func TestBuildSingleBenchConfigBrokenProfileIsAnError(t *testing.T) {
	pb := &ProxyBenchmarker{probeHosts: []string{"full:www.google.com"}}
	for _, c := range brokenProfiles {
		t.Run(c.name, func(t *testing.T) {
			entry := subscription.SubEntry{
				Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443,
				UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				RawOutbound: c.outbound,
				ProfileRaw:  c.profile,
			}
			if _, ok, err := pb.buildSingleBenchConfig(entry, benchInbounds(portPair{})); err == nil {
				t.Errorf("buildSingleBenchConfig() = %v, nil, want an error", ok)
			}
		})
	}
}

// E03: the ping goes through the same geo check as the session — a server or a
// profile whose rules name a missing list is refused, with the list named,
// before any core starts.
func TestMeasureRefusesMissingGeoLists(t *testing.T) {
	useSyntheticGeo(t)
	profile := torrentProfile(t)
	pb := &ProxyBenchmarker{probeHosts: []string{"full:www.google.com"}}
	entry := subscription.SubEntry{
		Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		RawOutbound: outboundNamed(t, "proxy-2"),
		ProfileRaw:  profile,
	}
	ports := portPair{socks: 41080, http: 41081}

	results := map[string]subscription.BenchmarkResult{
		"single server": pb.measureEntry(context.Background(), entry, ports),
		"whole profile": pb.measureProfileEntry(context.Background(),
			subscription.Profile{Name: "Auto", Raw: profile, Entries: []subscription.SubEntry{entry}}, ports),
	}
	for name, res := range results {
		if res.Error == nil || !strings.Contains(res.Error.Error(), "torrent") {
			t.Errorf("%s: error = %v, want a refusal naming geosite:torrent", name, res.Error)
		}
	}
}
