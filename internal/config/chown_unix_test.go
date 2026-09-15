//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// otherGID returns a supplementary group the user belongs to that is not the
// effective one, so a chown to it is both permitted (no root needed) and
// observable. Skips the test when there is no second group.
func otherGID(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	egid := os.Getegid()
	for _, g := range groups {
		if g != egid {
			return g
		}
	}
	t.Skip("need a supplementary group to observe a chown without root")
	return 0
}

func dirGID(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Sys().(*syscall.Stat_t).Gid)
}

// The data dir handback under sudo must not follow a symlink at the final
// component: os.Chown does, so a data dir the unprivileged user had replaced
// with a link to a root-owned directory (say /etc/cron.d) would be handed to
// them. The regression, shown with a plain os.Chown, changes the target's group;
// chownDirToSudoUser refuses and leaves it alone.
func TestChownDirToSudoUser_RefusesSymlink(t *testing.T) {
	gid := otherGID(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "xray-runner")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	before := dirGID(t, victim)

	// The old behavior, for contrast: following the link changes the target.
	if err := os.Chown(link, os.Geteuid(), gid); err != nil {
		t.Fatalf("os.Chown through the link: %v", err)
	}
	if dirGID(t, victim) != gid {
		t.Skip("chown to the chosen group is not observable here")
	}
	if err := os.Chown(victim, os.Geteuid(), before); err != nil {
		t.Fatal(err)
	}

	if err := chownDirToSudoUser(link, os.Geteuid(), gid); err == nil {
		t.Error("chownDirToSudoUser followed a symlink at the data dir path")
	}
	if got := dirGID(t, victim); got != before {
		t.Errorf("symlink target group changed to %d, want %d untouched", got, before)
	}
}

// A real directory is still handed over: the fix must not break the ordinary
// sudo case.
func TestChownDirToSudoUser_RealDir(t *testing.T) {
	gid := otherGID(t)
	dir := t.TempDir()
	if err := chownDirToSudoUser(dir, os.Geteuid(), gid); err != nil {
		t.Fatalf("chownDirToSudoUser on a real dir: %v", err)
	}
	if got := dirGID(t, dir); got != gid {
		t.Errorf("dir group = %d, want %d", got, gid)
	}
}

// A non-directory at the path is refused too: the data dir is a directory, and
// O_DIRECTORY keeps a planted file from being taken over.
func TestChownDirToSudoUser_RefusesNonDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := chownDirToSudoUser(file, os.Geteuid(), os.Getegid()); err == nil {
		t.Error("chownDirToSudoUser accepted a non-directory")
	}
}
