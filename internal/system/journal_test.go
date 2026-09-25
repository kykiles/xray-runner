package system

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"testing"
)

func TestMain(m *testing.M) {
	// No test may write the machine's real journal, nor take over what a real
	// crashed run left in it.
	journalAt = &memStore{m: map[int][]byte{}}
	os.Exit(m.Run())
}

// memStore keeps the records in memory.
type memStore struct{ m map[int][]byte }

func (s *memStore) put(pid int, data []byte) error { s.m[pid] = slices.Clone(data); return nil }
func (s *memStore) drop(pid int) error             { delete(s.m, pid); return nil }
func (s *memStore) all() (map[int][]byte, error)   { return maps.Clone(s.m), nil }

// withJournal gives the test an empty store and this process no record.
func withJournal(t *testing.T) *memStore {
	t.Helper()
	s := &memStore{m: map[int][]byte{}}
	origAt, origOwn, origWritten, origAlive := journalAt, ownJournal, written, processAlive
	journalAt, ownJournal, written = s, journal{}, false
	t.Cleanup(func() { journalAt, ownJournal, written, processAlive = origAt, origOwn, origWritten, origAlive })
	return s
}

// deadPID stands for the process that crashed.
const deadPID = 424242

// crash turns this process's record into a dead process's and forgets what
// this process knew about its changes: the store as the next run finds it.
func crash(t *testing.T, s *memStore) {
	t.Helper()
	j := readRecord(t, s, os.Getpid())
	if j == nil {
		t.Fatal("nothing was written down before the crash")
	}
	j.PID = deadPID
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.m, os.Getpid())
	s.m[deadPID] = data
	ownJournal, written, installed = journal{}, false, nil
	processAlive = func(pid int, _ uint64) bool { return pid != deadPID }
}

// readRecord is the record the store holds for pid, nil when there is none.
func readRecord(t *testing.T, s *memStore, pid int) *journal {
	t.Helper()
	data, ok := s.m[pid]
	if !ok {
		return nil
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatalf("record of %d: %v", pid, err)
	}
	return &j
}

// putRecord stores j under its pid, stamped with this boot unless it names one.
func putRecord(t *testing.T, s *memStore, j journal) {
	t.Helper()
	if j.Boot == "" {
		boot, err := bootID()
		if err != nil {
			t.Fatal(err)
		}
		j.Boot = boot
	}
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	s.m[j.PID] = data
}

func TestRecoverJournal_NothingWrittenDown(t *testing.T) {
	withJournal(t)
	note, err := RecoverJournal()
	if note != "" || err != nil {
		t.Errorf("RecoverJournal = %q, %v; want nothing", note, err)
	}
}

// A reboot takes every change a record names with it, and whatever now holds
// those routes is not the dead run's.
func TestRecoverJournal_DropsARecordOfAnEarlierBoot(t *testing.T) {
	s := withJournal(t)
	processAlive = func(int, uint64) bool { return false }
	putRecord(t, s, journal{Boot: "1", PID: deadPID, KillSwitch: true, Split: true})

	note, err := RecoverJournal()
	if note != "" || err != nil {
		t.Errorf("RecoverJournal = %q, %v; want nothing taken down", note, err)
	}
	if len(s.m) != 0 {
		t.Errorf("records left: %v", s.m)
	}
}

// Another session in progress — a second install, another user's run — is not
// a crash, and its changes are not ours to take down.
func TestRecoverJournal_LeavesARunningSessionAlone(t *testing.T) {
	s := withJournal(t)
	processAlive = func(int, uint64) bool { return true }
	putRecord(t, s, journal{PID: deadPID, KillSwitch: true, Split: true})

	note, err := RecoverJournal()
	if note != "" || err != nil {
		t.Errorf("RecoverJournal = %q, %v; want nothing taken down", note, err)
	}
	if readRecord(t, s, deadPID) == nil {
		t.Error("a running session's record was dropped")
	}
}

// A record that does not read back names nothing a teardown could remove
// exactly, and is not kept to trip every later run.
func TestRecoverJournal_DropsAnUnreadableRecord(t *testing.T) {
	s := withJournal(t)
	processAlive = func(int, uint64) bool { return false }
	s.m[deadPID] = []byte("{not json")
	putRecord(t, s, journal{PID: deadPID + 1, Routes: []routeRecord{{}}})

	note, err := RecoverJournal()
	if note != "" || err != nil {
		t.Errorf("RecoverJournal = %q, %v; want nothing taken down", note, err)
	}
	if len(s.m) != 0 {
		t.Errorf("records left: %v", s.m)
	}
}

// A process's record lives exactly as long as it has changes to name.
func TestNote_WritesWhileThereIsSomethingToName(t *testing.T) {
	s := withJournal(t)
	note(func(j *journal) { j.Split = true })
	j := readRecord(t, s, os.Getpid())
	if j == nil || !j.Split || j.PID != os.Getpid() || j.Boot == "" || j.Start == 0 {
		t.Fatalf("record = %+v, want this process's, naming the split", j)
	}
	note(func(j *journal) { j.Split = false })
	if len(s.m) != 0 {
		t.Errorf("records left once nothing is changed: %v", s.m)
	}
}
