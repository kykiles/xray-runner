//go:build !windows

package updater

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Unix only: Go implements Chmod on Windows through the read-only attribute
// alone, so a writable file always stats back as 0666 and an exact permission
// check there tests nothing. seal still runs on Windows — Chmod just has little
// to say about the result.
func TestSealSetsMode(t *testing.T) {
	dir := t.TempDir()
	f, err := stage(dir, geoipName)
	if err != nil {
		t.Fatal(err)
	}
	if err := seal(f, dir, geoipName, 0o644); err != nil {
		t.Fatalf("seal: %v", err)
	}
	fi, err := os.Stat(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode().Perm())
	}
}

// Under sudo the install directory belongs to the user, who could plant links
// under the fixed names the installer used to stage through. Writing, chmod or
// chown through such a link would reach a file of root's choosing (E02).
func TestInstallCore_PlantedLinksUntouched(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			victim := writeVictim(t)
			planted := []string{"xray.new", "xray.old"}
			for _, name := range planted {
				plant(t, kind, victim, filepath.Join(dir, name))
			}
			xrayPath := filepath.Join(dir, "xray")
			if err := os.WriteFile(xrayPath, []byte("OLD"), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := InstallCore(context.Background(), coreServer(t, "BINARY"), xrayPath); err != nil {
				t.Fatalf("InstallCore: %v", err)
			}

			assertVictim(t, victim)
			fi, err := os.Lstat(xrayPath)
			if err != nil {
				t.Fatal(err)
			}
			if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o755 {
				t.Errorf("xray mode = %v, want a regular 0755 file", fi.Mode())
			}
			if got := readFile(t, xrayPath); got != "BINARY" {
				t.Errorf("xray = %q, want BINARY", got)
			}
			for _, name := range planted {
				assertPlanted(t, kind, victim, filepath.Join(dir, name))
			}
			assertDirHolds(t, dir, append([]string{"xray"}, planted...)...)
		})
	}
}

// The geo databases were downloaded to TMPDIR and, when that is another
// filesystem (tmpfs /tmp against the install dir in $HOME), copied to the fixed
// name "<db>.new" with os.Create — through whatever link sat there.
func TestInstallGeo_PlantedLinksUntouched(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			victim := writeVictim(t)
			useOtherFilesystemTmp(t, dir)
			var planted []string
			for _, db := range []string{geoipName, geositeName} {
				for _, suffix := range []string{".new", ".old"} {
					planted = append(planted, db+suffix)
					plant(t, kind, victim, filepath.Join(dir, db+suffix))
				}
			}
			writeOldGeo(t, dir)

			geoip, geosite := geoServer(t, newGeo, nil)
			if err := InstallReleaseGeo(context.Background(), geoip, geosite, dir); err != nil {
				t.Fatalf("InstallReleaseGeo: %v", err)
			}

			assertVictim(t, victim)
			for _, db := range []string{geoipName, geositeName} {
				if got := readFile(t, filepath.Join(dir, db)); got != newGeo(db) {
					t.Errorf("%s = %q, want %q", db, got, newGeo(db))
				}
			}
			for _, name := range planted {
				assertPlanted(t, kind, victim, filepath.Join(dir, name))
			}
			assertDirHolds(t, dir, append([]string{geoipName, geositeName}, planted...)...)
		})
	}
}

const victimBody = "must survive"

// writeVictim makes the file a planted link points at, outside the install dir.
func writeVictim(t *testing.T) string {
	t.Helper()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte(victimBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return victim
}

func assertVictim(t *testing.T, victim string) {
	t.Helper()
	fi, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, victim); got != victimBody || fi.Mode().Perm() != 0o600 {
		t.Errorf("victim = %q mode %o, want %q mode 600 untouched", got, fi.Mode().Perm(), victimBody)
	}
}

func plant(t *testing.T, kind, target, at string) {
	t.Helper()
	var err error
	if kind == "symlink" {
		err = os.Symlink(target, at)
	} else {
		err = os.Link(target, at)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// assertPlanted fails unless the link planted at `at` is still there as it was.
func assertPlanted(t *testing.T, kind, target, at string) {
	t.Helper()
	fi, err := os.Lstat(at)
	if err != nil {
		t.Errorf("planted %s is gone: %v", filepath.Base(at), err)
		return
	}
	if kind == "symlink" {
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("planted %s is no longer a symlink: %v", filepath.Base(at), fi.Mode())
		}
		return
	}
	tfi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi, tfi) {
		t.Errorf("planted %s is no longer a link to the victim", filepath.Base(at))
	}
}

// useOtherFilesystemTmp points TMPDIR at a filesystem other than dir's, where
// the machine has one, so a download staged in TMPDIR cannot be renamed into
// dir and would have to be copied.
func useOtherFilesystemTmp(t *testing.T, dir string) {
	t.Helper()
	stat := func(path string) *syscall.Stat_t {
		fi, err := os.Stat(path)
		if err != nil {
			return nil
		}
		st, _ := fi.Sys().(*syscall.Stat_t)
		return st
	}
	own, other := stat(dir), stat("/dev/shm")
	if own == nil || other == nil || own.Dev == other.Dev {
		t.Log("no second filesystem at /dev/shm; downloads stay on the install one")
		return
	}
	tmp, err := os.MkdirTemp("/dev/shm", "updater-test-*")
	if err != nil {
		t.Logf("/dev/shm not writable (%v); downloads stay on the install filesystem", err)
		return
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("TMPDIR", tmp)
}

// The owner of an installed file comes from the file it replaces, or from the
// directory for a first install — never from SUDO_UID, which made a core in a
// root-owned install writable by the sudo user (G04).
func TestInstallOwner_FollowsReplacedFileNotSudo(t *testing.T) {
	t.Setenv("SUDO_UID", "4242")
	t.Setenv("SUDO_GID", "4242")
	orig := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = orig })

	dir := t.TempDir()
	want := func(path string) (int, int) {
		t.Helper()
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		return int(st.Uid), int(st.Gid)
	}

	uid, gid, ok := installOwner(dir, "xray")
	wu, wg := want(dir)
	if !ok || uid != wu || gid != wg {
		t.Errorf("first install: owner %d:%d ok=%v, want the dir's %d:%d", uid, gid, ok, wu, wg)
	}

	path := filepath.Join(dir, "xray")
	if err := os.WriteFile(path, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	uid, gid, ok = installOwner(dir, "xray")
	wu, wg = want(path)
	if !ok || uid != wu || gid != wg {
		t.Errorf("replace: owner %d:%d ok=%v, want the old file's %d:%d", uid, gid, ok, wu, wg)
	}
}

// End to end, as root: a new core takes the old core's owner.
func TestInstallCore_KeepsOldOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	t.Setenv("SUDO_UID", "4243")
	t.Setenv("SUDO_GID", "4243")
	dir := t.TempDir()
	xrayPath := filepath.Join(dir, "xray")
	if err := os.WriteFile(xrayPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(xrayPath, 4242, 4242); err != nil {
		t.Fatal(err)
	}

	if err := InstallCore(context.Background(), coreServer(t, "BINARY"), xrayPath); err != nil {
		t.Fatalf("InstallCore: %v", err)
	}

	fi, err := os.Stat(xrayPath)
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 4242 || st.Gid != 4242 {
		t.Errorf("new core owner %d:%d, want the old core's 4242:4242", st.Uid, st.Gid)
	}
}
