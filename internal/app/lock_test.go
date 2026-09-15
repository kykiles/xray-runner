package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"xray-runner/internal/config"
)

// newLockApp builds a minimal App wired for lock tests: its lock file in dir, as
// New puts it, and a config beside it for the tests of acquireLock alone —
// claimInstance moves the config into a runtime dir of its own. Apps built on
// the same dir contend for one lock, the way instances of one install do.
func newLockApp(dir string) *App {
	return &App{
		tmpFile:  filepath.Join(dir, "xray_config.json"),
		lockFile: filepath.Join(dir, "xray_config.json.lock"),
	}
}

// ownLock takes the lock in dir for the whole test. The cleanup lets it go
// before the temp dir is removed — Windows will not delete an open file.
func ownLock(t *testing.T, dir string) *App {
	t.Helper()
	a := newLockApp(dir)
	if err := a.acquireLock(); err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	t.Cleanup(a.cleanup)
	return a
}

// assertLockTaken fails if another instance could take the lock at lockFile.
func assertLockTaken(t *testing.T, lockFile string) {
	t.Helper()
	if lockProbe(t, lockFile).acquireLock() == nil {
		t.Error("another instance got the lock while its owner still holds it")
	}
}

// assertLockFree fails unless another instance can take the lock at lockFile.
func assertLockFree(t *testing.T, lockFile string) {
	t.Helper()
	if err := lockProbe(t, lockFile).acquireLock(); err != nil {
		t.Errorf("lock not free for the next instance: %v", err)
	}
}

// lockProbe contends for lockFile with a config of its own elsewhere, so its
// cleanup cannot take the config a test is watching.
func lockProbe(t *testing.T, lockFile string) *App {
	p := &App{tmpFile: filepath.Join(t.TempDir(), "probe.json"), lockFile: lockFile}
	t.Cleanup(p.cleanup)
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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

func ownPidLine() string { return strconv.Itoa(os.Getpid()) + "\n" }

// A07: an instance refused by a live lock used to delete the config of the
// instance that holds it — the core running there reads that file on its next
// restart. Since 11b the owner's config is in its runtime dir.
func TestCleanup_RefusedInstanceKeepsOwnersFiles(t *testing.T) {
	isolateRuntime(t)
	dir := t.TempDir()
	owner := ownClaim(t, dir)
	writeFile(t, owner.tmpFile, "active-instance-config")

	second := newLockApp(dir)
	err := second.claimInstance()
	if err == nil {
		t.Fatal("acquireLock should refuse while another instance holds the lock")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("refusal %q does not name the owner's pid %d", err, os.Getpid())
	}
	second.cleanup()

	assertFileHolds(t, owner.tmpFile, "active-instance-config")
	assertFileHolds(t, owner.lockFile, ownPidLine())
	assertLockTaken(t, owner.lockFile)
}

// The owner takes its config away and lets the lock go, but the lock file stays:
// unlinking it would let the next instance lock a new file while a third still
// has the old one open — two owners at once (11a). Until 11a, cleanup removed
// the lock file too, and this test checked that. Since 11b the config goes with
// the instance's runtime dir.
func TestCleanup_OwnerRemovesConfigKeepsLockFile(t *testing.T) {
	isolateRuntime(t)
	a := newLockApp(t.TempDir())
	if err := a.claimInstance(); err != nil {
		t.Fatalf("claimInstance: %v", err)
	}
	writeFile(t, a.tmpFile, "own-config")

	a.cleanup()

	if _, err := os.Stat(a.tmpFile); !os.IsNotExist(err) {
		t.Errorf("config left behind after the owner's cleanup (stat err = %v)", err)
	}
	if _, err := os.Stat(a.lockFile); err != nil {
		t.Errorf("lock file removed by cleanup: %v", err)
	}
	assertLockFree(t, a.lockFile)
}

// Once released, the lock belongs to whoever takes it next: a second cleanup
// must neither remove the next instance's config nor free its lock.
func TestCleanup_TwiceLeavesNextOwnersFiles(t *testing.T) {
	isolateRuntime(t)
	dir := t.TempDir()
	a := newLockApp(dir)
	if err := a.claimInstance(); err != nil {
		t.Fatalf("claimInstance: %v", err)
	}
	a.cleanup()

	next := ownClaim(t, dir)
	writeFile(t, next.tmpFile, "next-instance-config")
	a.cleanup()

	assertFileHolds(t, next.tmpFile, "next-instance-config")
	assertLockTaken(t, next.lockFile)
}

func TestAcquireLock_CleanDir(t *testing.T) {
	a := ownLock(t, t.TempDir())
	assertFileHolds(t, a.lockFile, ownPidLine())
}

// A lock file nobody holds is free, whatever it says: the pid in it only names
// the owner, it is no evidence that the owner still runs. A killed run leaves
// such a file, and since 11a so does every clean exit. Before, a live pid there
// — a recycled one, any process at all — kept every later run out.
func TestAcquireLock_UnheldFileIsFree(t *testing.T) {
	for name, body := range map[string]string{
		"empty":    "",
		"garbage":  "not-a-pid\n",
		"zero":     "0\n",
		"live pid": strconv.Itoa(os.Getppid()) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			a := newLockApp(t.TempDir())
			writeFile(t, a.lockFile, body)
			if err := a.acquireLock(); err != nil {
				t.Fatalf("lock file %q with no holder should be taken, got: %v", body, err)
			}
			t.Cleanup(a.cleanup)
			assertFileHolds(t, a.lockFile, ownPidLine())
		})
	}
}

// Between taking the lock and writing its pid the owner leaves an empty file.
// The pid protocol read that as stale and took the lock over from the running
// owner, so two instances shared one config (A07).
func TestAcquireLock_EmptyFileOfLiveOwnerRefused(t *testing.T) {
	dir := t.TempDir()
	owner := ownLock(t, dir)
	writeFile(t, owner.tmpFile, "active-instance-config")
	writeFile(t, owner.lockFile, "")

	second := newLockApp(dir)
	if err := second.acquireLock(); err == nil {
		t.Fatal("an empty lock file was taken over while its owner holds the lock")
	}
	second.cleanup()

	assertFileHolds(t, owner.tmpFile, "active-instance-config")
}

// A lock that cannot be taken for a reason other than a live owner is an error
// of its own, and the refused run leaves whatever sits at those paths alone.
func TestAcquireLock_OpenFailure(t *testing.T) {
	a := newLockApp(t.TempDir())
	if err := os.Mkdir(a.lockFile, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, a.tmpFile, "someone-elses-config")

	err := a.acquireLock()
	if err == nil {
		t.Fatal("acquireLock should fail when the lock file cannot be opened")
	}
	if strings.Contains(err.Error(), "другой экземпляр") {
		t.Errorf("error %q blames another instance for a lock file it could not open", err)
	}
	a.cleanup()

	assertFileHolds(t, a.tmpFile, "someone-elses-config")
	if fi, err := os.Stat(a.lockFile); err != nil || !fi.IsDir() {
		t.Errorf("the directory at the lock path was touched (stat err = %v)", err)
	}
}

// The pid is only a diagnostic, but a failed write of it is still an error, not
// dropped on the floor, and the lock taken for it is let go.
func TestAcquireLock_PidWriteFailureLetsGo(t *testing.T) {
	orig := writeLockPID
	writeLockPID = func(*os.File) error { return errors.New("disk full") }
	t.Cleanup(func() { writeLockPID = orig })

	a := newLockApp(t.TempDir())
	writeFile(t, a.tmpFile, "someone-elses-config")
	err := a.acquireLock()
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("acquireLock err = %v, want the pid write failure", err)
	}
	a.cleanup()
	writeLockPID = orig

	assertFileHolds(t, a.tmpFile, "someone-elses-config")
	assertLockFree(t, a.lockFile)
}

// In TUN mode root, or an elevated run on Windows, opens the lock file in the
// user's own directory and writes its pid into it. The pid protocol created the
// file exclusively and removed whatever it found; since 11a the file is opened
// as it is, so a link planted at the path must not carry the truncate to
// another file, or the create to another place.
func TestAcquireLock_DoesNotWriteThroughLinks(t *testing.T) {
	// Windows gives symlinks to administrators and developer mode only, and wine
	// reports one made without making it.
	plantOrSkip := func(t *testing.T, link string, err error) {
		t.Helper()
		if err == nil {
			_, err = os.Lstat(link)
		}
		if err != nil && runtime.GOOS == "windows" {
			t.Skipf("cannot plant the link here: %v", err)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, plant := range map[string]func(target, link string) error{
		"symlink":  os.Symlink,
		"hardlink": os.Link,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			victim := filepath.Join(dir, "victim")
			writeFile(t, victim, "must survive")
			a := newLockApp(dir)
			plantOrSkip(t, a.lockFile, plant(victim, a.lockFile))
			if err := a.acquireLock(); err == nil {
				a.cleanup()
				t.Error("acquireLock took a lock file that is a link to another file")
			}
			assertFileHolds(t, victim, "must survive")
		})
	}
	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "created-by-root")
		a := newLockApp(dir)
		plantOrSkip(t, a.lockFile, os.Symlink(target, a.lockFile))
		if err := a.acquireLock(); err == nil {
			a.cleanup()
			t.Error("acquireLock took a lock file that is a dangling symlink")
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Errorf("the open created the symlink's target (lstat err = %v)", err)
		}
	})
}

// Under sudo root creates the lock file, and since 11a it outlives the run:
// left root-owned 0600, it would keep the next run without sudo from opening
// it. The owner hands it back by descriptor, the way the log does.
func TestAcquireLock_HandsLockFileBackToSudoUser(t *testing.T) {
	orig := restoreLockOwner
	var got []string
	restoreLockOwner = func(f *os.File) error { got = append(got, f.Name()); return nil }
	t.Cleanup(func() { restoreLockOwner = orig })

	a := ownLock(t, t.TempDir())

	if len(got) != 1 || got[0] != a.lockFile {
		t.Errorf("lock file handed back as %q, want once as %q", got, a.lockFile)
	}
}

// The lock stays where 11a put it, beside where config.Path finds the config,
// so the runs of one install keep meeting at one lock; a stray lock file in the
// working directory still does not pull it there. The config itself has no
// path until claimInstance makes the runtime dir (11b) — until then this test
// checked that the lock sat next to the config.
func TestNew_LockStaysWhereTheConfigWas(t *testing.T) {
	for name, tc := range map[string]struct {
		plant string
		inCWD bool // a legacy config keeps the lock in the working directory
	}{
		"fresh":                {"", false},
		"stray lock in cwd":    {"xray_config.json.lock", false},
		"legacy config in cwd": {"xray_config.json", true},
	} {
		t.Run(name, func(t *testing.T) {
			isolateState(t)
			if tc.plant != "" {
				writeFile(t, tc.plant, "")
			}

			a := New(&config.Config{}, Options{})

			want := filepath.Join(config.DataDir(), "xray_config.json.lock")
			if tc.inCWD {
				want = "xray_config.json.lock"
			}
			if a.lockFile != want {
				t.Errorf("lock at %q, want %q", a.lockFile, want)
			}
			if a.tmpFile != "" {
				t.Errorf("config path %q chosen before the instance made its runtime dir", a.tmpFile)
			}
		})
	}
}
