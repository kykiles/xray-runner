package xraycfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// setGeo points the check at databases carrying category-ads and google
// (geosite) and private (geoip), for one test.
func setGeo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeDat(t, filepath.Join(dir, "geosite.dat"), "category-ads", "google")
	writeDat(t, filepath.Join(dir, "geoip.dat"), "private")
	SetGeoAssets(dir, "")
	t.Cleanup(func() { SetGeoAssets("", "") })
}

// E03: a list the databases lack is named in the error, by the database it is
// missing from. The lists that are there are not blamed.
func TestCheckGeoListsNamesMissingLists(t *testing.T) {
	setGeo(t)
	cases := []struct {
		name    string
		cfg     string
		want    []string
		notWant []string
	}{
		{
			name: "geosite in a block rule",
			cfg:  `{"routing": {"rules": [{"domain": ["geosite:torrent"], "outboundTag": "block"}]}}`,
			want: []string{"geosite.dat", "torrent"},
		},
		{
			name:    "geoip in a direct rule",
			cfg:     `{"routing": {"rules": [{"ip": ["geoip:private", "geoip:nowhere"], "outboundTag": "direct"}]}}`,
			want:    []string{"geoip.dat", "nowhere"},
			notWant: []string{"private", "geosite.dat"},
		},
		{
			name:    "mixed known and unknown matchers",
			cfg:     `{"routing": {"rules": [{"domain": ["geosite:google@ads", "geosite:nosuchlist", "example.com"], "outboundTag": "direct"}]}}`,
			want:    []string{"nosuchlist"},
			notWant: []string{"google", "example.com"},
		},
		{
			name:    "dns server",
			cfg:     `{"dns": {"servers": ["https://8.8.8.8/dns-query", {"address": "https://8.8.8.8/dns-query", "domains": ["geosite:twitch-ads", "geosite:google"]}]}}`,
			want:    []string{"geosite.dat", "twitch-ads"},
			notWant: []string{"google"},
		},
		{
			// xray reads a rule's string list from a comma-separated string too.
			name:    "comma-separated string",
			cfg:     `{"routing": {"rules": [{"domain": "geosite:google,geosite:torrent", "outboundTag": "block"}]}}`,
			want:    []string{"torrent"},
			notWant: []string{"google"},
		},
		{
			name: "both databases",
			cfg:  `{"routing": {"rules": [{"domain": ["geosite:torrent"], "outboundTag": "block"}, {"ip": ["geoip:nowhere"], "outboundTag": "direct"}]}}`,
			want: []string{"geosite.dat", "torrent", "geoip.dat", "nowhere"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckGeoLists(json.RawMessage(c.cfg))
			if err == nil {
				t.Fatalf("CheckGeoLists() = nil, want an error naming %v", c.want)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(err.Error(), w) {
					t.Errorf("error %q names %q, which is not missing", err, w)
				}
			}
		})
	}
}

func TestCheckGeoListsPassesKnownLists(t *testing.T) {
	setGeo(t)
	cfg := `{
		"dns": {"servers": ["1.1.1.1", {"address": "77.88.8.8", "domains": ["GeoSite:Google", "domain:ru"]}]},
		"routing": {"rules": [
			{"domain": ["geosite:category-ads@ads", "ext:custom.dat:x", "example.com"], "outboundTag": "block"},
			{"ip": ["geoip:private", "10.0.0.0/8"], "outboundTag": "direct"},
			{"port": "443", "outboundTag": "proxy"}
		]}
	}`
	if err := CheckGeoLists(json.RawMessage(cfg)); err != nil {
		t.Fatalf("CheckGeoLists() = %v, want nil: every list is in the databases", err)
	}
}

// A rule the check cannot read is not waved through as checked; xray would not
// accept it either.
func TestCheckGeoListsRejectsUnreadableRules(t *testing.T) {
	setGeo(t)
	for _, cfg := range []string{
		`{"routing": {"rules": 5}}`,
		`{"routing": {"rules": [{"domain": 5, "outboundTag": "block"}]}}`,
		`{"dns": {"servers": [{"address": "1.1.1.1", "domains": 5}]}}`,
	} {
		if err := CheckGeoLists(json.RawMessage(cfg)); err == nil {
			t.Errorf("CheckGeoLists(%s) = nil, want an error", cfg)
		}
	}
}

// Without readable databases there is nothing to check against: a core found on
// PATH keeps its databases elsewhere, and xray itself judges the config then.
func TestCheckGeoListsWithoutAssets(t *testing.T) {
	cfg := json.RawMessage(`{"routing": {"rules": [{"domain": ["geosite:torrent"], "outboundTag": "block"}]}}`)
	SetGeoAssets("", "")
	if err := CheckGeoLists(cfg); err != nil {
		t.Errorf("no databases set: CheckGeoLists() = %v, want nil", err)
	}
	SetGeoAssets(t.TempDir(), "")
	t.Cleanup(func() { SetGeoAssets("", "") })
	if err := CheckGeoLists(cfg); err != nil {
		t.Errorf("databases unreadable: CheckGeoLists() = %v, want nil", err)
	}
}

// E03: the databases in use are not always the ones the rules were written
// against — an update of the panel's has failed. A refusal says so next to the
// missing lists, so the reason reaches the screen and not only the log; a
// config that passes is not refused over it.
func TestCheckGeoListsSaysWhyTheseDatabases(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, filepath.Join(dir, "geosite.dat"), "google")
	writeDat(t, filepath.Join(dir, "geoip.dat"), "private")
	SetGeoAssets(dir, "Гео-базы панели не обновились: сеть недоступна.")
	t.Cleanup(func() { SetGeoAssets("", "") })

	err := CheckGeoLists(json.RawMessage(`{"routing": {"rules": [{"domain": ["geosite:torrent"], "outboundTag": "block"}]}}`))
	if err == nil || !strings.Contains(err.Error(), "torrent") || !strings.Contains(err.Error(), "не обновились") {
		t.Errorf("CheckGeoLists() = %v, want the missing list and why these databases", err)
	}
	if err := CheckGeoLists(json.RawMessage(`{"routing": {"rules": [{"domain": ["geosite:google"], "outboundTag": "direct"}]}}`)); err != nil {
		t.Errorf("known list: CheckGeoLists() = %v, want nil", err)
	}
}

// E03: the merge leaves the panel's routing and dns exactly as they came. The
// old filter dropped unknown lists, and a rule stripped of one matcher could
// cover more traffic than the panel meant it to.
func TestMergeProfileKeepsRoutingAndDNS(t *testing.T) {
	setGeo(t)
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
			{"ip": ["geoip:private", "geoip:nowhere"], "outboundTag": "direct"},
			{"port": "443", "outboundTag": "proxy"}
		]}
	}`

	merged, err := MergeProfile(json.RawMessage(raw), nil, "warning")
	if err != nil {
		t.Fatal(err)
	}

	var in, out map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"routing", "dns"} {
		if !reflect.DeepEqual(in[k], out[k]) {
			t.Errorf("%s changed by the merge:\n got %v\nwant %v", k, out[k], in[k])
		}
	}
	if err := CheckGeoLists(merged); err == nil || !strings.Contains(err.Error(), "torrent") {
		t.Errorf("CheckGeoLists(merged) = %v, want the missing lists named", err)
	}
}

// A service session checks against the databases its interface handed over,
// not the ones in use: a list only they carry passes, and a list they lack is
// named with their folder.
func TestCheckGeoListsIn(t *testing.T) {
	setGeo(t)
	dir := t.TempDir()
	writeDat(t, filepath.Join(dir, "geosite.dat"), "ru-blocked")
	writeDat(t, filepath.Join(dir, "geoip.dat"), "ru")
	cfg := json.RawMessage(`{"routing":{"rules":[{"domain":["geosite:ru-blocked"],"outboundTag":"proxy"},{"ip":["geoip:ru"],"outboundTag":"direct"}]}}`)
	if err := CheckGeoLists(cfg); err == nil {
		t.Fatal("the databases in use carry ru-blocked: the test checks nothing")
	}
	if err := CheckGeoListsIn(cfg, dir, ""); err != nil {
		t.Errorf("CheckGeoListsIn = %v, want nil", err)
	}
	missing := json.RawMessage(`{"routing":{"rules":[{"domain":["geosite:torrent"],"outboundTag":"block"}]}}`)
	err := CheckGeoListsIn(missing, dir, "")
	if err == nil || !strings.Contains(err.Error(), "torrent") || !strings.Contains(err.Error(), dir) {
		t.Errorf("CheckGeoListsIn = %v, want torrent named with %s", err, dir)
	}
}

// What the service is handed is a geo database from end to end, or refused.
func TestCheckGeoDatabase(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.dat")
	writeDat(t, good, "ru-blocked", "torrent")
	if err := CheckGeoDatabase(good); err != nil {
		t.Errorf("a database refused: %v", err)
	}
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"empty":     nil,
		"text":      []byte("#!/bin/sh\nexit 0\n"),
		"truncated": data[:len(data)-3],
		"trailing":  append(append([]byte{}, data...), 0xFF),
		"nameless":  {0x0A, 0x02, 0x10, 0x01},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeoDatabase(path); err == nil {
				t.Error("not a database, and taken")
			}
		})
	}
}
