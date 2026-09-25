package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
)

// A run that dies without its teardown — SIGKILL, a crash, a console window
// closed faster than Windows lets a process clean up — leaves on the host what
// it changed there. The tun device goes with the core, and the routes through
// it with the device; the rest stays until a reboot: the server exceptions, on
// Linux also the direct table, the mark rule, the IPv6 block, the kill switch's
// chain and the split's table and cgroup. That kept IPv6 off, or the whole
// network with the kill switch, and made the next TUN session refuse to start
// over routes it could not tell from somebody else's.
//
// So each change is written down in a record of this process's own before it
// is made, and the record goes once nothing in it is left. The next run takes
// over the records of processes that are gone — in this boot: a reboot took
// their changes with it — and tears those changes down the way their own
// teardown would have (H06).

// journal is one process's record: what it may have changed and not yet taken
// back.
type journal struct {
	Boot string `json:"boot"`
	PID  int    `json:"pid"`
	// Start is when the process started, so that a pid the system has since
	// given to another process is not taken for the one that wrote the record.
	Start      uint64            `json:"start"`
	Routes     []routeRecord     `json:"routes,omitempty"`
	KillSwitch bool              `json:"kill_switch,omitempty"`
	Split      bool              `json:"split,omitempty"`
	SplitHome  map[string]string `json:"split_home,omitempty"`
}

func (j *journal) empty() bool { return len(j.Routes) == 0 && !j.KillSwitch && !j.Split }

// journalStore keeps the records, one per process, keyed by pid.
type journalStore interface {
	put(pid int, data []byte) error
	drop(pid int) error
	all() (map[int][]byte, error)
}

var (
	journalMu sync.Mutex
	// ownJournal is this process's record as last written; written reports
	// whether there is one in the store to replace or drop.
	ownJournal journal
	written    bool
	// journalAt is where the records live, nil when this process has nowhere to
	// keep one (see newJournalStore). Overridable in tests.
	journalAt = newJournalStore()
	// processAlive reports whether the process that wrote a record still runs.
	// Overridable in tests.
	processAlive = sameProcessAlive
)

// note applies one change to this process's record and writes it out: before
// a change to the host, so that dying in the middle of it leaves it written
// down, and after one is taken back, so the record names nothing that is gone.
// A record that ends up empty is dropped. A failed write costs the safety net,
// not the change the caller came for, so it is logged rather than returned.
func note(update func(*journal)) {
	journalMu.Lock()
	defer journalMu.Unlock()
	update(&ownJournal)
	if err := saveJournal(); err != nil {
		slog.Warn("журнал изменений системы не записан", "error", err)
	}
}

func saveJournal() error {
	if journalAt == nil {
		return nil
	}
	pid := os.Getpid()
	if ownJournal.empty() {
		if !written {
			return nil
		}
		written = false
		return journalAt.drop(pid)
	}
	if ownJournal.PID == 0 {
		boot, err := bootID()
		if err != nil {
			return err
		}
		start, err := procStart(pid)
		if err != nil {
			return err
		}
		ownJournal.Boot, ownJournal.PID, ownJournal.Start = boot, pid, start
	}
	data, err := json.Marshal(&ownJournal)
	if err != nil {
		return err
	}
	if err := journalAt.put(pid, data); err != nil {
		return err
	}
	written = true
	return nil
}

// journalRoutes records the TUN routes this process owns, or may be adding.
func journalRoutes(entries []tunEntry) {
	records := routeRecords(entries)
	note(func(j *journal) { j.Routes = records })
}

func routeRecords(entries []tunEntry) []routeRecord {
	records := make([]routeRecord, len(entries))
	for i, e := range entries {
		records[i] = e.record()
	}
	return records
}

// RecoverJournal takes down what runs that died without their teardown left on
// the host. It is called once, before the first session, and returns a line
// for the status screen — empty when there was nothing to take down.
//
// A record of this boot whose process is gone is taken over: its changes join
// this process's own record before the old one is dropped, so dying halfway
// through loses nothing, and are then taken down by the same teardown a session
// runs, which removes only what is still there as it was added. A record of an
// earlier boot is dropped: the reboot took its changes. A record whose process
// still runs belongs to a session in progress and is left alone.
func RecoverJournal() (string, error) {
	if journalAt == nil {
		return "", nil
	}
	records, err := journalAt.all()
	if err != nil {
		return "", fmt.Errorf("журнал изменений системы не прочитан: %w", err)
	}
	if len(records) == 0 {
		return "", nil
	}
	boot, err := bootID()
	if err != nil {
		return "", fmt.Errorf("журнал изменений системы: %w", err)
	}

	self := os.Getpid()
	var found journal
	var taken []int
	for pid, data := range records {
		var j journal
		if err := json.Unmarshal(data, &j); err != nil || j.PID != pid {
			slog.Warn("нечитаемая запись журнала изменений системы удалена", "pid", pid, "error", err)
			taken = append(taken, pid)
			continue
		}
		if !sameBoot(j.Boot, boot) {
			taken = append(taken, pid)
			continue
		}
		if processAlive(j.PID, j.Start) {
			continue
		}
		routes, err := restoreRoutes(j.Routes)
		if err != nil {
			// Nothing in it is acted on: an entry that does not read back is not
			// something a teardown can name exactly.
			slog.Warn("запись журнала изменений системы не разобрана и удалена", "pid", pid, "error", err)
			taken = append(taken, pid)
			continue
		}
		slog.Warn("остатки аварийно завершённого запуска", "pid", pid, "routes", len(routes), "kill_switch", j.KillSwitch, "split", j.Split)
		for _, e := range routes {
			// Two records naming one route would have the teardown delete it
			// twice, and fail on the second.
			if !slices.Contains(installed, e) {
				installed = append(installed, e)
			}
		}
		found.Routes = append(found.Routes, j.Routes...)
		found.KillSwitch = found.KillSwitch || j.KillSwitch
		if j.Split {
			found.Split = true
			found.SplitHome = mergeSplitHome(found.SplitHome, j.SplitHome)
		}
		taken = append(taken, pid)
	}

	if !found.empty() {
		records := routeRecords(installed)
		note(func(j *journal) {
			j.Routes = records
			j.KillSwitch = j.KillSwitch || found.KillSwitch
			j.Split = j.Split || found.Split
		})
	}
	for _, pid := range taken {
		// A record under our own pid was left by an earlier boot, or by the
		// process whose pid we were given. When something was taken over, note
		// above has already put ours in its place.
		if pid == self && !found.empty() {
			continue
		}
		if err := journalAt.drop(pid); err != nil {
			slog.Warn("запись журнала изменений системы не удалена", "pid", pid, "error", err)
		}
	}
	if found.empty() {
		return "", nil
	}

	var what []string
	var errs []error
	if len(found.Routes) > 0 {
		what = append(what, "маршруты TUN")
		if err := DisableTunRouting(); err != nil {
			errs = append(errs, err)
		}
	}
	if found.KillSwitch {
		what = append(what, "kill switch")
		if err := recoverKillSwitch(); err != nil {
			errs = append(errs, err)
		}
	}
	if found.Split {
		what = append(what, "раздельная маршрутизация")
		if err := recoverSplit(found.SplitHome); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("остатки аварийно завершённого запуска (%s) сняты не полностью: %w", strings.Join(what, ", "), errors.Join(errs...))
	}
	return "Сняты остатки аварийно завершённого запуска: " + strings.Join(what, ", ") + ".", nil
}

func mergeSplitHome(into, from map[string]string) map[string]string {
	if into == nil {
		into = map[string]string{}
	}
	maps.Copy(into, from)
	return into
}
