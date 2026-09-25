package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xray-runner/internal/config"
)

func TestSudoOwnerAbsentWithoutSudo(t *testing.T) {
	t.Setenv("SUDO_UID", "")
	t.Setenv("SUDO_GID", "")
	if _, _, ok := sudoOwner(); ok {
		t.Error("sudoOwner reported a user with no sudo environment")
	}

	// A half-set environment must not be treated as a valid owner either:
	// chowning to gid 0 would be worse than leaving the files alone.
	t.Setenv("SUDO_UID", "1000")
	if _, _, ok := sudoOwner(); ok {
		t.Error("sudoOwner accepted SUDO_UID without SUDO_GID")
	}

	t.Setenv("SUDO_GID", "1000")
	uid, gid, ok := sudoOwner()
	if !ok || uid != 1000 || gid != 1000 {
		t.Errorf("sudoOwner = %d, %d, %v; want 1000, 1000, true", uid, gid, ok)
	}
}

// The regression this guards: reclaim used to walk the working directory, which
// under sudo hands over whatever tree the user launched from — /etc for a binary
// on PATH. Nothing outside the app's own data dir may be walked.
func TestReclaimWalksOnlyOwnDirs(t *testing.T) {
	data := config.DataDir()
	for _, dir := range reclaimedDirs() {
		switch {
		case dir == "" || dir == data:
		case filepath.IsAbs(dir) || dir == "." || strings.Contains(dir, ".."):
			t.Errorf("reclaimedDirs contains %q, which is not the app's own directory", dir)
		}
	}
}

// recordChowns makes reclaimDir report the names it hands over instead of
// changing owners, so the walk can be checked without root.
func recordChowns(t *testing.T) *[]string {
	t.Helper()
	var got []string
	orig := rootLchown
	rootLchown = func(_ *os.Root, name string, _, _ int) error {
		got = append(got, name)
		return nil
	}
	t.Cleanup(func() { rootLchown = orig })
	return &got
}

// The walk hands over what is in the directory, a symlink as itself, and
// nothing that lives outside it (G03).
func TestReclaimDir_StaysInsideTheDir(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "other"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs", "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "sub", "s.json"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	got := recordChowns(t)

	reclaimDir(dir, 1000, 1000)

	want := []string{".", "configs", "configs/sub", "configs/sub/s.json", "link"}
	if !slices.Equal(*got, want) {
		t.Errorf("handed over %q, want %q", *got, want)
	}
}

// A directory that is itself a symlink is not walked at all.
func TestReclaimDir_SkipsASymlinkedDir(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "f"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "configs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	got := recordChowns(t)

	reclaimDir(link, 1000, 1000)

	if len(*got) != 0 {
		t.Errorf("walked a symlinked dir: %q", *got)
	}
}
