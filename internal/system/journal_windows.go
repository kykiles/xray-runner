//go:build windows

package system

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// newJournalStore picks where this process keeps its record: under HKLM, which
// only administrators may write. A record anyone could plant would have the
// next elevated run remove the routes it names. A run without elevation changes
// nothing a record would name and cannot take anything down, so it keeps none
// and recovers none; the next elevated run does.
func newJournalStore() journalStore {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return regStore{root: registry.LOCAL_MACHINE, path: `SOFTWARE\xray-runner`}
}

// regStore keeps each record as a string value named by the pid. The key goes
// when its last value does, so a machine without a crashed run carries none.
type regStore struct {
	root registry.Key
	path string
}

func (s regStore) put(pid int, data []byte) error {
	var err error
	// Twice: another run may drop the key, its last value gone, between the
	// create and the write.
	for range 2 {
		var k registry.Key
		k, _, err = registry.CreateKey(s.root, s.path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue(strconv.Itoa(pid), string(data))
		_ = k.Close()
		if !errors.Is(err, windows.ERROR_KEY_DELETED) {
			return err
		}
	}
	return err
}

func (s regStore) drop(pid int) error {
	k, err := registry.OpenKey(s.root, s.path, registry.SET_VALUE|registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = k.DeleteValue(strconv.Itoa(pid))
	if errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	info, statErr := k.Stat()
	_ = k.Close()
	if err == nil && statErr == nil && info.ValueCount == 0 && info.SubKeyCount == 0 {
		// Best-effort: a key left empty costs nothing.
		_ = registry.DeleteKey(s.root, s.path)
	}
	return err
}

func (s regStore) all() (map[int][]byte, error) {
	k, err := registry.OpenKey(s.root, s.path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = k.Close() }()
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, err
	}
	out := map[int][]byte{}
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		v, _, err := k.GetStringValue(name)
		if err != nil {
			return nil, err
		}
		out[pid] = []byte(v)
	}
	return out, nil
}

// bootID is when this boot started, in Unix seconds. Windows names a boot by
// no stable id, so it is worked out from the uptime, and two readings of it
// differ by however the clock was set in between.
func bootID() (string, error) {
	return strconv.FormatInt(time.Now().Add(-windows.DurationSinceBoot()).Unix(), 10), nil
}

// bootSlack is how far two readings of bootID may drift apart and still name
// one boot. A clock that moved further makes the record look like an earlier
// boot's: it is dropped, and what it named is left for the manual cleanup it
// needed before the journal existed.
const bootSlack = 2 * 60

func sameBoot(a, b string) bool {
	x, err1 := strconv.ParseInt(a, 10, 64)
	y, err2 := strconv.ParseInt(b, 10, 64)
	return err1 == nil && err2 == nil && x-y <= bootSlack && y-x <= bootSlack
}

// procStart is the process's creation time, in the FILETIME's 100-nanosecond
// ticks.
func procStart(pid int) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

// stillActive is the exit code of a process that has not exited.
const stillActive = 259

// sameProcessAlive reports whether the process that wrote a record still runs:
// its pid is taken, by a process that started when it did. A process that is
// there but cannot be looked into counts as running — a record is only ever
// taken over from a process known to be gone.
func sameProcessAlive(pid int, start uint64) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER: no process has the pid.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	if code != stillActive {
		return false
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return true
	}
	return uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) == start
}

// routeRecord is a tunEntry as the journal keeps it.
type routeRecord struct {
	Prefix  string `json:"prefix"`
	IfIndex int    `json:"if"`
	NextHop string `json:"next_hop"`
	SelfHop bool   `json:"self_hop,omitempty"`
}

func (e tunEntry) record() routeRecord {
	return routeRecord{Prefix: e.prefix.String(), IfIndex: e.ifIndex, NextHop: e.nextHop.String(), SelfHop: e.selfHop}
}

// restoreRoutes reads the records back into entries a teardown can name, and
// refuses the lot when one of them is not something EnableTunRouting adds.
// Every value ends up in a PowerShell script, and gets there as a netip value
// or an integer only.
func restoreRoutes(records []routeRecord) ([]tunEntry, error) {
	out := make([]tunEntry, 0, len(records))
	for _, r := range records {
		what := "запись журнала"
		p, err := netip.ParsePrefix(r.Prefix)
		if err != nil || p != p.Masked() || p.Addr().Zone() != "" {
			return nil, fmt.Errorf("%s: неверный префикс %q", what, r.Prefix)
		}
		hop, err := netip.ParseAddr(r.NextHop)
		if err != nil || hop.Is4() != p.Addr().Is4() || hop.Zone() != "" {
			return nil, fmt.Errorf("%s: неверный next hop %q", what, r.NextHop)
		}
		if r.IfIndex <= 0 {
			return nil, fmt.Errorf("%s: неверный индекс интерфейса %d", what, r.IfIndex)
		}
		out = append(out, tunEntry{what: what, prefix: p, ifIndex: r.IfIndex, nextHop: hop, selfHop: r.SelfHop})
	}
	return out, nil
}

// recoverSplit has nothing to take down: the split mode here is the tunnel's
// routes and a rule inside xray (ADR-0003), and a record never says Split.
func recoverSplit(map[string]string) error { return nil }
