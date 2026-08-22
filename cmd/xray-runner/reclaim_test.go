package main

import (
	"path/filepath"
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
