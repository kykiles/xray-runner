package xraycfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeDat builds a .dat with the given list names: one length-delimited entry
// per name, each starting with the name as field 1 — the shape both real
// databases have.
func writeDat(t *testing.T, path string, names ...string) {
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

func TestDropUnknownGeo(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, filepath.Join(dir, "geosite.dat"), "category-ads", "google")
	writeDat(t, filepath.Join(dir, "geoip.dat"), "ru")
	SetGeoAssets(dir)
	t.Cleanup(func() { SetGeoAssets("") })

	raw := `{
		"outbounds": [{"tag": "proxy", "protocol": "freedom"}],
		"dns": {"servers": [
			"https://8.8.8.8/dns-query",
			{"address": "https://8.8.8.8/dns-query", "domains": ["geosite:twitch-ads", "geosite:google"]},
			{"address": "https://77.88.8.8/dns-query", "domains": ["geosite:nosuchlist"]}
		]},
		"routing": {"rules": [
			{"domain": ["geosite:torrent"], "outboundTag": "block"},
			{"domain": ["geosite:google@ads", "geosite:nosuchlist", "example.com"], "outboundTag": "direct"},
			{"ip": ["geoip:ru", "geoip:nowhere"], "outboundTag": "direct"},
			{"port": "443", "outboundTag": "proxy"}
		]}
	}`

	merged, err := MergeProfile(json.RawMessage(raw), nil, "warning")
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
		DNS struct {
			Servers []any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}

	// The plain address stays, the mixed server keeps its known list, and the
	// server left without domains goes — it would have answered every query.
	if len(got.DNS.Servers) != 2 {
		t.Fatalf("dns servers = %d, want 2: %v", len(got.DNS.Servers), got.DNS.Servers)
	}
	if srv, ok := got.DNS.Servers[1].(map[string]any); !ok || !equalList(srv["domains"], "geosite:google") {
		t.Errorf("dns server = %v", got.DNS.Servers[1])
	}

	// The torrent-only rule loses its last matcher and goes with it; a rule left
	// with no matcher at all would match everything.
	if len(got.Routing.Rules) != 3 {
		t.Fatalf("rules = %d, want 3: %v", len(got.Routing.Rules), got.Routing.Rules)
	}
	if d := got.Routing.Rules[0]["domain"]; !equalList(d, "geosite:google@ads", "example.com") {
		t.Errorf("domain = %v", d)
	}
	if ip := got.Routing.Rules[1]["ip"]; !equalList(ip, "geoip:ru") {
		t.Errorf("ip = %v", ip)
	}
	if got.Routing.Rules[2]["port"] != "443" {
		t.Errorf("untouched rule lost: %v", got.Routing.Rules[2])
	}
}

// Without the databases nothing is filtered: guessing would strip valid rules.
func TestDropUnknownGeoWithoutAssets(t *testing.T) {
	SetGeoAssets("")
	raw := `{"outbounds":[{"tag":"proxy"}],"routing":{"rules":[{"domain":["geosite:torrent"],"outboundTag":"block"}]}}`
	merged, err := MergeProfile(json.RawMessage(raw), nil, "warning")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Routing.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(got.Routing.Rules))
	}
}

func equalList(v any, want ...string) bool {
	list, ok := v.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, w := range want {
		if list[i] != w {
			return false
		}
	}
	return true
}
