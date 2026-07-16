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
		lockFile: filepath.Join(dir, "xray_config.json.lock"),
		pidAlive: alive,
	}
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
