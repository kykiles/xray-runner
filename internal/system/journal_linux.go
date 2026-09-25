//go:build linux

package system

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// newJournalStore picks where this process keeps its record. Root's goes to
// /run, which only root can write: a record anyone could plant would have the
// next root run delete the routes it names and move processes where it says.
// It is tmpfs, emptied on boot, like everything the record names. A run with
// capabilities instead of root changes the host too (split, see
// splitCgroupPath) and keeps its record in its own cache directory, where the
// boot id tells a stale one apart.
func newJournalStore() journalStore {
	if os.Geteuid() == 0 {
		return dirStore{dir: "/run/xray-runner"}
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return dirStore{dir: filepath.Join(base, "xray-runner", "journal")}
}

// dirStore keeps each record as <pid>.json in dir, replaced whole by a rename
// so a death halfway through a write leaves the previous record readable.
type dirStore struct{ dir string }

func (s dirStore) name(pid int) string { return strconv.Itoa(pid) + ".json" }

func (s dirStore) put(pid int, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	r, err := os.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	tmp := s.name(pid) + ".tmp"
	if err := r.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return r.Rename(tmp, s.name(pid))
}

func (s dirStore) drop(pid int) error {
	err := os.Remove(filepath.Join(s.dir, s.name(pid)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s dirStore) all() (map[int][]byte, error) {
	r, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	entries, err := fs.ReadDir(r.FS(), ".")
	if err != nil {
		return nil, err
	}
	out := map[int][]byte{}
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		pid, err := strconv.Atoi(base)
		if !ok || err != nil || pid <= 0 || !e.Type().IsRegular() {
			continue
		}
		data, err := r.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		out[pid] = data
	}
	return out, nil
}

// bootID names this boot of the kernel.
func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("boot id не прочитан: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func sameBoot(a, b string) bool { return a == b }

// procStart is the process's start time, in clock ticks since boot: field 22
// of /proc/<pid>/stat. It is counted after the command name, which is in
// parentheses and may hold spaces and parentheses itself.
func procStart(pid int) (uint64, error) {
	// Not procRoot, which split tests point at a fake tree.
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("/proc/%d/stat: нет имени команды", pid)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("/proc/%d/stat: мало полей", pid)
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

// sameProcessAlive reports whether the process that wrote a record still runs:
// its pid is taken, by a process that started when it did. A pid that cannot
// be looked up for another reason counts as running — a record is only ever
// taken over from a process known to be gone.
func sameProcessAlive(pid int, start uint64) bool {
	got, err := procStart(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return err != nil || got == start
}

// routeRecord is a tunEntry as the journal keeps it.
type routeRecord struct {
	Rule   bool   `json:"rule,omitempty"`
	Pref   int    `json:"pref,omitempty"`
	V6     bool   `json:"v6,omitempty"`
	Type   string `json:"type,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Via    string `json:"via,omitempty"`
	Dev    string `json:"dev,omitempty"`
	Table  string `json:"table,omitempty"`
	Metric int    `json:"metric,omitempty"`
}

func (e tunEntry) record() routeRecord {
	if e.rule {
		return routeRecord{Rule: true, Pref: e.pref}
	}
	return routeRecord{V6: e.v6, Type: e.typ, Prefix: e.prefix.String(), Via: e.via, Dev: e.dev, Table: e.table, Metric: e.metric}
}

// devName is what an interface name may look like: the kernel's IFNAMSIZ, and
// nothing ip could read as an option.
var devName = regexp.MustCompile(`^[A-Za-z0-9_.:@][A-Za-z0-9_.:@-]{0,14}$`)

// restoreRoutes reads the records back into entries a teardown can name, and
// refuses the lot when one of them is not something EnableTunRouting adds.
func restoreRoutes(records []routeRecord) ([]tunEntry, error) {
	out := make([]tunEntry, 0, len(records))
	for _, r := range records {
		what := "запись журнала"
		if r.Rule {
			if r.Pref <= 0 {
				return nil, fmt.Errorf("%s: правило без приоритета", what)
			}
			out = append(out, tunEntry{what: what, rule: true, pref: r.Pref})
			continue
		}
		p, err := netip.ParsePrefix(r.Prefix)
		if err != nil || p != p.Masked() || p.Addr().Is4() == r.V6 {
			return nil, fmt.Errorf("%s: неверный префикс %q", what, r.Prefix)
		}
		if r.Via != "" {
			if a, err := netip.ParseAddr(r.Via); err != nil || a.Is4() == r.V6 {
				return nil, fmt.Errorf("%s: неверный шлюз %q", what, r.Via)
			}
		}
		if !devName.MatchString(r.Dev) {
			return nil, fmt.Errorf("%s: неверное имя интерфейса %q", what, r.Dev)
		}
		if r.Type != "" && r.Type != "unreachable" {
			return nil, fmt.Errorf("%s: неверный тип маршрута %q", what, r.Type)
		}
		if _, err := strconv.Atoi(r.Table); r.Table != "" && err != nil {
			return nil, fmt.Errorf("%s: неверная таблица %q", what, r.Table)
		}
		if r.Metric < 0 {
			return nil, fmt.Errorf("%s: неверная метрика %d", what, r.Metric)
		}
		out = append(out, tunEntry{what: what, v6: r.V6, typ: r.Type, prefix: p, via: r.Via, dev: r.Dev, table: r.Table, metric: r.Metric})
	}
	return out, nil
}

// recoverSplit puts the processes a dead run moved into the split cgroup back
// where the record says they came from, and takes the ruleset down.
func recoverSplit(home map[string]string) error {
	splitMu.Lock()
	for pid, h := range home {
		// A path under the hierarchy root, and nothing that climbs out of it:
		// DisableSplit writes the pid into <root>/<home>/cgroup.procs.
		if _, err := strconv.Atoi(pid); err == nil && strings.HasPrefix(h, "/") && filepath.IsLocal(h[1:]) {
			splitHome[pid] = h
		}
	}
	splitMu.Unlock()
	return errors.Join(DisableSplit(), dropLegacySplit())
}
