//go:build linux

package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// H06: a run killed with the tunnel up leaves the server exception, the direct
// table, the mark rule and the IPv6 block behind. The next run finds them
// written down, takes them down, and its own session then starts where it used
// to refuse over them.
func TestRecoverJournal_TakesDownACrashedRunsRoutes(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)
	s := withJournal(t)
	// What the host looks like once the crashed run's tun is gone and nothing
	// of its routing is left.
	clean := cleanHost()
	clean.tunDied()

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	crash(t, s)
	f.tunDied()

	note, err := RecoverJournal()
	if err != nil {
		t.Fatalf("RecoverJournal: %v", err)
	}
	if !strings.Contains(note, "маршруты TUN") {
		t.Errorf("note %q does not say the routes were taken down", note)
	}
	if got, want := f.state(), clean.state(); got != want {
		t.Errorf("host after recovery:\n%s\nwant nothing of the crashed run's:\n%s", got, want)
	}
	if len(s.m) != 0 {
		t.Errorf("records left after a full recovery: %v", s.m)
	}

	// The next core brings its tun up — the host is then the clean one, as
	// checked above — and the session starts where it used to refuse over the
	// leftovers.
	f.routes = cleanHost().routes
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("the session after the recovery: %v", err)
	}
}

// An entry is written down before its add runs, so a kill between the add and
// the next line still leaves it named.
func TestEnableTunRouting_WritesEachEntryDownBeforeAddingIt(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)
	s := withJournal(t)
	adds := 0
	f.before = func(cmd string) {
		if !strings.Contains(cmd, " add ") {
			return
		}
		adds++
		j := readRecord(t, s, os.Getpid())
		if j == nil || len(j.Routes) != adds {
			t.Errorf("at add %d (%s) the record names %d entries", adds, cmd, len(j.Routes))
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

// A teardown that could not remove an entry keeps it written down, for the
// next run to try again.
func TestDisableTunRouting_KeepsWhatItFailedToRemoveWrittenDown(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)
	s := withJournal(t)
	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.fail = func(_ int, cmd string) bool { return strings.Contains(cmd, "45.150.32.235") }
	if err := DisableTunRouting(); err == nil {
		t.Fatal("a failed delete went unreported")
	}
	j := readRecord(t, s, os.Getpid())
	if j == nil || len(j.Routes) != 1 || j.Routes[0].Prefix != "45.150.32.235/32" {
		t.Errorf("record after a partial teardown = %+v, want only the server exception", j)
	}
}

// A killed run with the kill switch on leaves the machine without a network
// until a reboot. The next run takes the chain down.
func TestRecoverJournal_TakesDownACrashedRunsKillSwitch(t *testing.T) {
	fw := withFakeNft(t)
	s := withJournal(t)

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}
	crash(t, s)

	note, err := RecoverJournal()
	if err != nil {
		t.Fatalf("RecoverJournal: %v", err)
	}
	if !strings.Contains(note, "kill switch") {
		t.Errorf("note %q does not name the kill switch", note)
	}
	if fw.table != nil {
		t.Errorf("the table is still there after the recovery: %v", fw.table)
	}
	if len(s.m) != 0 {
		t.Errorf("records left: %v", s.m)
	}
}

// A kill switch that will not come down stays written down, under this run
// now, and the screen hears of it.
func TestRecoverJournal_KeepsAKillSwitchItCouldNotRemove(t *testing.T) {
	fw := withFakeNft(t)
	s := withJournal(t)
	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}
	crash(t, s)
	fw.stuck = true

	if _, err := RecoverJournal(); err == nil {
		t.Fatal("a kill switch left in place went unreported")
	}
	if j := readRecord(t, s, os.Getpid()); j == nil || !j.KillSwitch {
		t.Errorf("record = %+v, want this run to carry the kill switch on", j)
	}
	if readRecord(t, s, deadPID) != nil {
		t.Error("the dead run's record is still there beside ours")
	}
}

// The processes a killed run moved into the split cgroup keep redirecting into
// its dead port. The next run puts them back where they came from.
func TestRecoverJournal_PutsSplitProcessesBack(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	fw := nftOps.(*fakeNft)
	s := withJournal(t)
	home := filepath.Join(cgroupRoot, "user.slice", "app-code.scope")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	crash(t, s)
	clear(splitHome)

	note, err := RecoverJournal()
	if err != nil {
		t.Fatalf("RecoverJournal: %v", err)
	}
	if !strings.Contains(note, "раздельная маршрутизация") {
		t.Errorf("note %q does not name the split", note)
	}
	if back, _ := os.ReadFile(filepath.Join(home, "cgroup.procs")); strings.TrimSpace(string(back)) != "42" {
		t.Errorf("pid put back into %q, want its original scope", back)
	}
	if _, err := os.Stat(splitCgroup); !os.IsNotExist(err) {
		t.Error("split cgroup not removed")
	}
	if fw.table != nil {
		t.Errorf("the ruleset was not taken down: %v", fw.table)
	}
	if len(s.m) != 0 {
		t.Errorf("records left: %v", s.m)
	}
}

// Only root writes the record, but nothing in it is trusted further than
// EnableTunRouting's own entries go: an entry that is not one of them refuses
// the whole record, and a split home outside the hierarchy is not written to.
func TestRestoreRoutes_RefusesWhatEnableNeverAdds(t *testing.T) {
	for name, r := range map[string]routeRecord{
		"rule without priority":  {Rule: true},
		"unmasked prefix":        {Prefix: "10.0.0.1/8", Dev: "eth0"},
		"family mismatch":        {V6: true, Prefix: "10.0.0.0/8", Dev: "eth0"},
		"gateway of other kind":  {Prefix: "10.0.0.0/8", Via: "fe80::1", Dev: "eth0"},
		"option for a device":    {Prefix: "10.0.0.0/8", Dev: "-6"},
		"device too long":        {Prefix: "10.0.0.0/8", Dev: "abcdefghijklmnop"},
		"type Enable never adds": {Prefix: "10.0.0.0/8", Dev: "eth0", Type: "prohibit"},
		"table by name":          {Prefix: "10.0.0.0/8", Dev: "eth0", Table: "main"},
	} {
		if _, err := restoreRoutes([]routeRecord{{Prefix: "1.2.3.4/32", Dev: "eth0"}, r}); err == nil {
			t.Errorf("%s: %+v accepted", name, r)
		}
	}

	withFakes(t, nil)
	outside := filepath.Join(filepath.Dir(cgroupRoot), "cgroup.procs")
	if err := recoverSplit(map[string]string{"42": "/../" + filepath.Base(filepath.Dir(outside))}); err != nil {
		t.Fatalf("recoverSplit: %v", err)
	}
	if len(splitHome) != 0 {
		t.Errorf("a home outside the hierarchy was taken: %v", splitHome)
	}
}

func TestDirStore(t *testing.T) {
	s := dirStore{dir: filepath.Join(t.TempDir(), "journal")}
	if got, err := s.all(); err != nil || len(got) != 0 {
		t.Fatalf("all() on a missing dir = %v, %v", got, err)
	}
	if err := s.put(7, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.put(7, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := s.put(8, []byte("three")); err != nil {
		t.Fatal(err)
	}
	got, err := s.all()
	if err != nil || len(got) != 2 || string(got[7]) != "two" || string(got[8]) != "three" {
		t.Fatalf("all() = %q, %v", got, err)
	}
	fi, err := os.Stat(filepath.Join(s.dir, "7.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, %v; want 0600", fi.Mode(), err)
	}
	if di, err := os.Stat(s.dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", di.Mode(), err)
	}
	if err := s.drop(7); err != nil {
		t.Fatal(err)
	}
	if err := s.drop(7); err != nil {
		t.Errorf("dropping a missing record: %v", err)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 1 || entries[0].Name() != "8.json" {
		t.Errorf("dir holds %v, want only 8.json", entries)
	}
}

// A pid the system handed to another process is not the one that wrote the
// record: the start time tells them apart.
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
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	if sameProcessAlive(cmd.Process.Pid, start) {
		t.Error("an exited process reads as running")
	}
}
