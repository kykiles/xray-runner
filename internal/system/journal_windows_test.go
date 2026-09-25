//go:build windows

package system

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// H06: a run killed with the tunnel up loses its adapter, and the routes on it
// with it, but its server exceptions on the physical adapter stay — and the
// next session refused to start over them. The next run finds them written
// down and takes them down, leaving the routes on the new adapter alone.
func TestRecoverJournal_TakesDownACrashedRunsRoutes(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	s := withJournal(t)
	clean := winHost()
	clean.recreateTun(31)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	crash(t, s)
	f.recreateTun(31)

	note, err := RecoverJournal()
	if err != nil {
		t.Fatalf("RecoverJournal: %v", err)
	}
	if !strings.Contains(note, "маршруты TUN") {
		t.Errorf("note %q does not say the routes were taken down", note)
	}
	if got, want := f.state(), clean.state(); got != want {
		t.Errorf("routes after recovery:\n%s\nwant nothing of the crashed run's:\n%s", got, want)
	}
	if len(s.m) != 0 {
		t.Errorf("records left after a full recovery: %v", s.m)
	}

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("the session after the recovery: %v", err)
	}
}

// A route is written down before its add runs, so a kill between the add and
// the next line still leaves it named.
func TestEnableTunRouting_WritesEachRouteDownBeforeAddingIt(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	s := withJournal(t)
	adds := 0
	f.before = func(cmd string) {
		adds++
		j := readRecord(t, s, os.Getpid())
		if j == nil || len(j.Routes) != adds {
			t.Errorf("at add %d (%s) the record names %+v", adds, cmd, j)
		}
	}
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if adds == 0 {
		t.Fatal("no adds seen")
	}
	f.before = nil
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	if len(s.m) != 0 {
		t.Errorf("records left after the teardown: %v", s.m)
	}
}

// Every value of a record ends up in a PowerShell script: what is not a route
// EnableTunRouting adds refuses the whole record.
func TestRestoreRoutes_RefusesWhatEnableNeverAdds(t *testing.T) {
	good := routeRecord{Prefix: "1.2.3.4/32", IfIndex: 12, NextHop: "192.168.31.1"}
	for name, r := range map[string]routeRecord{
		"unmasked prefix":   {Prefix: "10.0.0.1/8", IfIndex: 12, NextHop: "0.0.0.0"},
		"not a prefix":      {Prefix: "1.2.3.4'; Remove-Item", IfIndex: 12, NextHop: "0.0.0.0"},
		"hop of other kind": {Prefix: "10.0.0.0/8", IfIndex: 12, NextHop: "fe80::1"},
		"no interface":      {Prefix: "10.0.0.0/8", NextHop: "0.0.0.0"},
	} {
		if _, err := restoreRoutes([]routeRecord{good, r}); err == nil {
			t.Errorf("%s: %+v accepted", name, r)
		}
	}
	if got, err := restoreRoutes([]routeRecord{good}); err != nil || len(got) != 1 || got[0].record() != good {
		t.Errorf("restoreRoutes(%+v) = %+v, %v", good, got, err)
	}
}

// The store is exercised under HKCU: HKLM needs elevation, and a test must not
// write the machine's real journal.
func TestRegStore(t *testing.T) {
	s := regStore{root: registry.CURRENT_USER, path: `Software\xray-runner-test-` + strconv.FormatInt(time.Now().UnixNano(), 10)}
	t.Cleanup(func() { _ = registry.DeleteKey(s.root, s.path) })

	if got, err := s.all(); err != nil || len(got) != 0 {
		t.Fatalf("all() on a missing key = %v, %v", got, err)
	}
	for _, v := range []struct {
		pid  int
		data string
	}{{7, "one"}, {7, "two"}, {8, "three"}} {
		if err := s.put(v.pid, []byte(v.data)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.all()
	if err != nil || len(got) != 2 || string(got[7]) != "two" || string(got[8]) != "three" {
		t.Fatalf("all() = %q, %v", got, err)
	}
	for _, pid := range []int{7, 7, 8} {
		if err := s.drop(pid); err != nil {
			t.Fatalf("drop(%d): %v", pid, err)
		}
	}
	// A machine without a crashed run carries no key.
	if _, err := registry.OpenKey(s.root, s.path, registry.QUERY_VALUE); !errors.Is(err, registry.ErrNotExist) {
		t.Errorf("key left once its last value went: %v", err)
	}
}

// A pid Windows handed to another process is not the one that wrote the
// record: the creation time tells them apart.
func TestSameProcessAlive(t *testing.T) {
	start, err := procStart(os.Getpid())
	if err != nil || start == 0 {
		t.Fatalf("procStart(self) = %d, %v", start, err)
	}
	if !sameProcessAlive(os.Getpid(), start) {
		t.Error("this process reads as gone")
	}
	if sameProcessAlive(os.Getpid(), start+1) {
		t.Error("a process started at another time reads as the one that wrote the record")
	}
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	if sameProcessAlive(cmd.Process.Pid, start) {
		t.Error("an exited process reads as running")
	}
}

func TestSameBoot(t *testing.T) {
	now, _ := bootID()
	n, _ := strconv.ParseInt(now, 10, 64)
	for _, c := range []struct {
		other string
		same  bool
	}{
		{now, true},
		{strconv.FormatInt(n+30, 10), true},
		{strconv.FormatInt(n-bootSlack-1, 10), false},
		{"", false},
	} {
		if got := sameBoot(c.other, now); got != c.same {
			t.Errorf("sameBoot(%q, %q) = %v, want %v", c.other, now, got, c.same)
		}
	}
}
