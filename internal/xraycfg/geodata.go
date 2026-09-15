package xraycfg

// A panel's routing names geo lists its own databases carry, and ours are not
// the same build: Loyalsoldier's geosite.dat has no "torrent" list, so a rule
// saying geosite:torrent makes xray refuse the entire config. Reading the list
// names straight out of the .dat files lets the tool say which lists are
// missing, and from which database, before a core is started (E03). The config
// itself is never rewritten: a rule stripped of one matcher can select more
// than the panel meant, and a dropped block rule lets through what it stopped.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// geoAssets is the directory holding geosite.dat/geoip.dat and why it is the
// one in use, as passed to SetGeoAssets. Empty (the default, and what tests get)
// checks nothing.
var geoAssets struct {
	sync.Mutex
	dir   string
	why   string
	lists func() map[string]bool
}

// SetGeoAssets points the geo check at xray's database directory. why, when set,
// says why those databases are in use — an update of the panel's has failed —
// and a refusal carries it after the missing lists. Reading the files is
// deferred to the first check that needs them.
func SetGeoAssets(dir, why string) {
	geoAssets.Lock()
	defer geoAssets.Unlock()
	geoAssets.dir, geoAssets.why = dir, why
	if dir == "" {
		geoAssets.lists = nil
		return
	}
	geoAssets.lists = sync.OnceValue(func() map[string]bool {
		known := map[string]bool{}
		for _, f := range []string{"geosite.dat", "geoip.dat"} {
			names, err := geoListNames(filepath.Join(dir, f))
			if err != nil {
				// Unreadable databases mean "check nothing": a core found on PATH
				// keeps its databases elsewhere, and xray judges the config itself.
				slog.Warn("geo database unreadable, geo lists in routing not checked", "file", f, "error", err)
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
// "GEOSITE:CN" / "GEOIP:RU", the directory they were read from and why it is in
// use. A nil map means nothing to check against.
func knownGeoLists() (map[string]bool, string, string) {
	geoAssets.Lock()
	f, dir, why := geoAssets.lists, geoAssets.dir, geoAssets.why
	geoAssets.Unlock()
	if f == nil {
		return nil, dir, why
	}
	return f(), dir, why
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

// CheckGeoLists refuses a config whose routing rules or dns servers name a geo
// list the databases do not carry, naming every missing list under the database
// it is missing from. The config is left as it is. Without readable databases
// there is nothing to check against and nil is returned; the core's own start
// has the last word then, as it has on everything else.
func CheckGeoLists(raw json.RawMessage) error {
	known, dir, why := knownGeoLists()
	if len(known) == 0 {
		return nil
	}

	var cfg struct {
		Routing struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"routing"`
		DNS struct {
			Servers []json.RawMessage `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("проверка гео-списков: %w", err)
	}

	var refs []string
	for _, r := range cfg.Routing.Rules {
		for _, field := range []string{"domain", "domains", "ip"} {
			list, err := stringList(r[field])
			if err != nil {
				return fmt.Errorf("проверка гео-списков: правило маршрутизации, поле %s: %w", field, err)
			}
			refs = append(refs, list...)
		}
	}
	for _, s := range cfg.DNS.Servers {
		var srv struct {
			Domains json.RawMessage `json:"domains"`
		}
		// A plain "https://8.8.8.8/dns-query" string names no lists.
		if json.Unmarshal(s, &srv) != nil {
			continue
		}
		list, err := stringList(srv.Domains)
		if err != nil {
			return fmt.Errorf("проверка гео-списков: dns-сервер, поле domains: %w", err)
		}
		refs = append(refs, list...)
	}

	missing := map[string][]string{}
	for _, ref := range refs {
		if db, name, ok := unknownGeo(ref, known); ok && !slices.Contains(missing[db], name) {
			missing[db] = append(missing[db], name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var parts []string
	for _, db := range []string{"geosite.dat", "geoip.dat"} {
		if len(missing[db]) > 0 {
			parts = append(parts, db+" — "+strings.Join(missing[db], ", "))
		}
	}
	err := fmt.Errorf("в гео-базах (%s) нет списков, на которые ссылаются правила маршрутизации или DNS: %s", dir, strings.Join(parts, "; "))
	if why != "" {
		err = fmt.Errorf("%w. %s", err, why)
	}
	return err
}

// stringList reads a rule's list field, which xray takes either as an array or
// as one comma-separated string.
func stringList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("не список строк")
	}
	return strings.Split(s, ","), nil
}

// unknownGeo reports the database and the list name of an entry naming a geo
// list the databases do not have. Anything else — a plain domain, a CIDR, an
// ext: file — is not ours to check.
func unknownGeo(s string, known map[string]bool) (db, name string, ok bool) {
	upper := strings.ToUpper(strings.TrimSpace(s))
	prefix, db := "GEOSITE:", "geosite.dat"
	name, found := strings.CutPrefix(upper, prefix)
	if !found {
		prefix, db = "GEOIP:", "geoip.dat"
		if name, found = strings.CutPrefix(upper, prefix); !found {
			return "", "", false
		}
	}
	// geoip:!cn matches everything outside the list, and geosite:google@ads
	// narrows a list by attribute; either way the list is what has to exist.
	name = strings.TrimPrefix(name, "!")
	name, _, _ = strings.Cut(name, "@")
	if known[prefix+name] {
		return "", "", false
	}
	return db, strings.ToLower(name), true
}
