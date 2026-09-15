//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// isolateRuntime points the runtime base at a fresh directory of the test's.
func isolateRuntime(t *testing.T) string {
	t.Helper()
	base := privateTempDir(t)
	t.Setenv("TMPDIR", base)
	return base
}

// privateTempDir is t.TempDir without the group write a umask of 002 leaves on
// it, which checkTrustedDirs rightly refuses.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	chmod(t, dir, 0o700)
	return dir
}

// assertRuntimePrivate: the dir is ours and 0700, the config ours and 0600.
func assertRuntimePrivate(t *testing.T, dir, file string) {
	t.Helper()
	for path, want := range map[string]os.FileMode{dir: 0o700, file: 0o600} {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
		if uid := fi.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Geteuid() {
			t.Errorf("%s: owner uid %d, want %d", path, uid, os.Geteuid())
		}
	}
}

// A base someone else can change under the running core is refused, with no
// fallback anywhere else: whoever can rename a directory on the way to the
// config can put a config of their own in its place before the core's next
// restart — root's core, in a TUN session.
func TestClaim_RefusesUntrustedRuntimeBase(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) string{
		"world-writable": func(t *testing.T) string {
			base := t.TempDir()
			chmod(t, base, 0o777)
			return base
		},
		"group-writable parent": func(t *testing.T) string {
			parent := t.TempDir()
			chmod(t, parent, 0o770)
			base := filepath.Join(parent, "tmp")
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			return base
		},
		// Under sudo the user's own directories are what root must not trust:
		// sudo -E hands TMPDIR over.
		"user-owned under root": func(t *testing.T) string {
			orig := geteuid
			geteuid = func() int { return 0 }
			t.Cleanup(func() { geteuid = orig })
			return privateTempDir(t)
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := setup(t)
			t.Setenv("TMPDIR", base)
			a := newLockApp(t.TempDir())

			err := a.claimInstance()
			if err == nil {
				a.cleanup()
				t.Fatal("claimInstance accepted a runtime base others can change")
			}
			if !strings.Contains(err.Error(), base) {
				t.Errorf("refusal %q does not name the base %q", err, base)
			}
			if a.runDir != "" {
				t.Errorf("runtime dir %q made after the refusal", a.runDir)
			}
			a.cleanup()

			if entries, _ := os.ReadDir(base); len(entries) != 0 {
				t.Errorf("refused base got %d entries", len(entries))
			}
			assertLockFree(t, a.lockFile)
		})
	}
}

// The ordinary shared /tmp is sticky: others may add entries there but not
// touch ours. A base reached through a symlink is used where it really is.
func TestClaim_AcceptsStickyAndLinkedBase(t *testing.T) {
	t.Run("sticky", func(t *testing.T) {
		base := t.TempDir()
		chmod(t, base, 0o777|os.ModeSticky)
		t.Setenv("TMPDIR", base)
		a := ownClaim(t, t.TempDir())
		if filepath.Dir(a.runDir) != base {
			t.Errorf("runtime dir %q, want in %q", a.runDir, base)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		real := privateTempDir(t)
		link := filepath.Join(t.TempDir(), "tmp")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", link)
		a := ownClaim(t, t.TempDir())
		if filepath.Dir(a.runDir) != real {
			t.Errorf("runtime dir %q, want in the link's target %q", a.runDir, real)
		}
	})
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
