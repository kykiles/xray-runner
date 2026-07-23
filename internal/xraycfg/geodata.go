package xraycfg

// A panel's routing names geo lists its own databases carry, and ours are not
// the same build: Loyalsoldier's geosite.dat has no "torrent" list, so a rule
// saying geosite:torrent makes xray refuse the entire config — no session, no
// ping, nothing. Reading the list names straight out of the .dat files lets the
// profile merge drop exactly those references and keep the rest of the panel's
// rules.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// geoAssets is the directory holding geosite.dat/geoip.dat, as passed to
// SetGeoAssets. Empty (the default, and what tests get) filters nothing.
var geoAssets struct {
	sync.Mutex
	dir   string
	lists func() map[string]bool
}

// SetGeoAssets points the geo filter at xray's database directory. Reading the
// files is deferred to the first merge that needs them.
func SetGeoAssets(dir string) {
	geoAssets.Lock()
	defer geoAssets.Unlock()
	geoAssets.dir = dir
	geoAssets.lists = sync.OnceValue(func() map[string]bool {
		known := map[string]bool{}
		for _, f := range []string{"geosite.dat", "geoip.dat"} {
			names, err := geoListNames(filepath.Join(dir, f))
			if err != nil {
				// Unreadable databases mean "filter nothing": xray will complain
				// about them itself, and guessing here would strip valid rules.
				slog.Warn("geo database unreadable, routing rules left as they are", "file", f, "error", err)
				return nil
			}
			for _, n := range names {
				known[strings.ToUpper(f[:len(f)-4])+":"+n] = true
			}
		}
		return known
	})
}

// knownGeoLists returns the list names both .dat files carry, keyed as
// "GEOSITE:CN" / "GEOIP:RU". nil means no filtering.
func knownGeoLists() map[string]bool {
	geoAssets.Lock()
	f := geoAssets.lists
	geoAssets.Unlock()
	if f == nil {
		return nil
	}
	return f()
}

// geoListNames reads the list names from a .dat file. Both databases are a
// protobuf message with a single repeated field, and every element starts with
// its name — enough structure to walk without linking xray's protos.
func geoListNames(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for i := 0; i < len(data); {
		key, n := varint(data, i)
		if n < 0 || key>>3 != 1 || key&7 != 2 {
			break
		}
		size, n := varint(data, n)
		if n < 0 || size > uint64(len(data)-n) {
			break
		}
		entry := data[n : n+int(size)]
		i = n + int(size)

		// Inside an entry the name is field 1, again length-delimited.
		key, m := varint(entry, 0)
		if m < 0 || key>>3 != 1 || key&7 != 2 {
			continue
		}
		size, m = varint(entry, m)
		if m < 0 || size > uint64(len(entry)-m) {
			continue
		}
		names = append(names, strings.ToUpper(string(entry[m:m+int(size)])))
	}
	return names, nil
}

// varint decodes one protobuf varint, returning the value and the offset just
// past it; a negative offset means the data ran out.
func varint(b []byte, i int) (uint64, int) {
	var v uint64
	for shift := 0; i < len(b) && shift <= 63; shift += 7 {
		c := b[i]
		i++
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i
		}
	}
	return 0, -1
}

// ruleMatchers are the fields that make a routing rule select traffic. A rule
// stripped of all of them matches *everything*, so it must go rather than stay
// behind as a catch-all pointing at the panel's blackhole.
var ruleMatchers = []string{"domain", "domains", "ip", "port", "sourcePort", "network", "source", "user", "inboundTag", "protocol", "attrs"}

// dropUnknownGeo removes references to geo lists our databases do not carry from
// the config's routing rules, returning how many rules it touched.
//
// ponytail: routing rules only — dns servers can name geosite lists too, but the
// panels seen so far keep them out of dns. Extend here if one shows up.
func dropUnknownGeo(cfg map[string]json.RawMessage) int {
	known := knownGeoLists()
	if len(known) == 0 || len(cfg["routing"]) == 0 {
		return 0
	}

	var routing map[string]json.RawMessage
	if err := json.Unmarshal(cfg["routing"], &routing); err != nil || len(routing["rules"]) == 0 {
		return 0
	}
	var rules []map[string]json.RawMessage
	if err := json.Unmarshal(routing["rules"], &rules); err != nil {
		return 0
	}

	touched := 0
	kept := make([]map[string]json.RawMessage, 0, len(rules))
	for _, r := range rules {
		if !stripUnknownGeo(r, known) {
			kept = append(kept, r)
			continue
		}
		touched++
		if slices.ContainsFunc(ruleMatchers, func(k string) bool { return len(r[k]) > 0 }) {
			kept = append(kept, r)
		}
	}
	if touched == 0 {
		return 0
	}

	encRules, err := json.Marshal(kept)
	if err != nil {
		return 0
	}
	routing["rules"] = encRules
	encRouting, err := json.Marshal(routing)
	if err != nil {
		return 0
	}
	cfg["routing"] = encRouting
	return touched
}

// stripUnknownGeo drops the unknown geosite:/geoip: entries of one rule and
// reports whether it changed anything.
func stripUnknownGeo(rule map[string]json.RawMessage, known map[string]bool) bool {
	changed := false
	for _, field := range []string{"domain", "domains", "ip"} {
		var list []string
		if len(rule[field]) == 0 || json.Unmarshal(rule[field], &list) != nil {
			continue
		}
		out := slices.DeleteFunc(slices.Clone(list), func(s string) bool { return isUnknownGeo(s, known) })
		if len(out) == len(list) {
			continue
		}
		changed = true
		if len(out) == 0 {
			delete(rule, field)
			continue
		}
		if enc, err := json.Marshal(out); err == nil {
			rule[field] = enc
		}
	}
	return changed
}

// isUnknownGeo reports whether a rule entry names a geo list the databases do
// not have. Anything else — a plain domain, a CIDR, an ext: file — is left alone.
func isUnknownGeo(s string, known map[string]bool) bool {
	name, ok := strings.CutPrefix(strings.ToUpper(s), "GEOSITE:")
	prefix := "GEOSITE:"
	if !ok {
		if name, ok = strings.CutPrefix(strings.ToUpper(s), "GEOIP:"); !ok {
			return false
		}
		prefix = "GEOIP:"
	}
	// geosite:google@ads narrows a list by attribute; the list is what has to
	// exist.
	name, _, _ = strings.Cut(name, "@")
	return !known[prefix+name]
}
