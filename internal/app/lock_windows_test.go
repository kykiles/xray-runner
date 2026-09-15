//go:build windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction needs no privilege: an unelevated process of the user can swap the
// data dir for one pointing anywhere, and an elevated run would then create its
// lock file there and write into it (11b).
func TestAcquireLock_RefusesJunctionedDir(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, victim).CombinedOutput(); err != nil {
		t.Skipf("cannot make a junction here: %v %s", err, out)
	}

	a := newLockApp(link)
	if err := a.acquireLock(); err == nil {
		a.cleanup()
		t.Error("acquireLock took a lock file through a junction")
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Errorf("the junction's target got %d entries", len(entries))
	}
}
