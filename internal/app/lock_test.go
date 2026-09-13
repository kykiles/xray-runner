package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newLockApp builds a minimal App wired for lock tests: its lock file lives in a
// fresh temp dir and process liveness is stubbed so the outcome is deterministic.
func newLockApp(t *testing.T, alive func(int) bool) *App {
	t.Helper()
	dir := t.TempDir()
	return &App{
		tmpFile:  filepath.Join(dir, "xray_config.json"),
		lockFile: filepath.Join(dir, "xray_config.json.lock"),
		pidAlive: alive,
	}
}

// writeInstanceFiles plants another instance's config and lock, the way a run
// that holds the lock leaves them on disk.
func writeInstanceFiles(t *testing.T, a *App, config, lock string) {
	t.Helper()
	if err := os.WriteFile(a.tmpFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.lockFile, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertFileHolds fails unless the file is still there with exactly this content.
func assertFileHolds(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", filepath.Base(path), got, want)
	}
}

// A07: an instance refused by a live lock used to delete the config of the
// instance that holds it — the core running there reads that file on its next
// restart.
func TestCleanup_RefusedInstanceKeepsOwnersFiles(t *testing.T) {
	a := newLockApp(t, func(int) bool { return true })
	writeInstanceFiles(t, a, "active-instance-config", "4242\n")

	if err := a.acquireLock(); err == nil {
		t.Fatal("acquireLock should refuse when the owner process is alive")
	}
	a.cleanup()

	assertFileHolds(t, a.tmpFile, "active-instance-config")
	assertFileHolds(t, a.lockFile, "4242\n")
}

func TestCleanup_OwnerRemovesItsConfigAndLock(t *testing.T) {
	a := newLockApp(t, func(int) bool { return true })
	if err := a.acquireLock(); err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	if err := os.WriteFile(a.tmpFile, []byte("own-config"), 0o600); err != nil {
		t.Fatal(err)
	}

	a.cleanup()

	for _, p := range []string{a.tmpFile, a.lockFile} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind after the owner's cleanup (stat err = %v)", filepath.Base(p), err)
		}
	}
	if a.lockHeld {
		t.Error("lockHeld still set after the lock was released")
	}
}

// Once released, the lock belongs to whoever takes it next: a second cleanup
// must not remove the next instance's lock and config.
func TestCleanup_TwiceLeavesNextOwnersFiles(t *testing.T) {
	a := newLockApp(t, func(int) bool { return true })
	if err := a.acquireLock(); err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	a.cleanup()

	writeInstanceFiles(t, a, "next-instance-config", "4343\n")
	a.cleanup()

	assertFileHolds(t, a.tmpFile, "next-instance-config")
	assertFileHolds(t, a.lockFile, "4343\n")
}

func TestAcquireLock_CleanDir(t *testing.T) {
	a := newLockApp(t, func(int) bool { return true })
	if err := a.acquireLock(); err != nil {
		t.Fatalf("acquireLock on clean dir: %v", err)
	}
	if !a.lockHeld {
		t.Error("lockHeld should be true after a successful acquire")
	}
	data, err := os.ReadFile(a.lockFile)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != os.Getpid() {
		t.Errorf("lock file should hold our pid %d, got %q", os.Getpid(), data)
	}
}

func TestAcquireLock_StalePidReclaimed(t *testing.T) {
	a := newLockApp(t, func(int) bool { return false }) // owner is dead
	if err := os.WriteFile(a.lockFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.acquireLock(); err != nil {
		t.Fatalf("stale lock should be reclaimed, got: %v", err)
	}
	if !a.lockHeld {
		t.Error("lockHeld should be true after reclaiming a stale lock")
	}
}

func TestAcquireLock_LivePidRefused(t *testing.T) {
	a := newLockApp(t, func(int) bool { return true }) // owner is alive
	if err := os.WriteFile(a.lockFile, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.acquireLock(); err == nil {
		t.Fatal("acquireLock should refuse when the owner process is alive")
	}
	if a.lockHeld {
		t.Error("lockHeld must stay false when the lock is refused")
	}
}

func TestAcquireLock_BrokenLockReclaimed(t *testing.T) {
	// An empty or garbage lock file (e.g. a crash between create and write)
	// carries no usable pid, so it must be treated as stale, not live.
	for _, body := range []string{"", "not-a-pid\n", "0\n"} {
		a := newLockApp(t, func(int) bool { return true })
		if err := os.WriteFile(a.lockFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := a.acquireLock(); err != nil {
			t.Fatalf("broken lock %q should be reclaimed, got: %v", body, err)
		}
	}
}
