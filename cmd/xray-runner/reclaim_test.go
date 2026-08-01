package main

import (
	"path/filepath"
	"testing"
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

// The guard is the load-bearing part: a recursive chown of $HOME or / is far
// worse than the permission error it is meant to prevent.
func TestSafeToReclaimRefusesHugeTrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, dir := range []string{"", "/", home} {
		if safeToReclaim(dir) {
			t.Errorf("safeToReclaim(%q) = true, want false", dir)
		}
	}
	if work := filepath.Join(home, "xray_linux"); !safeToReclaim(work) {
		t.Errorf("safeToReclaim(%q) = false, want true", work)
	}
}
