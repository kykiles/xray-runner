//go:build !windows

package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// Unix only: Go implements Chmod on Windows through the read-only attribute
// alone, so a writable file always stats back as 0666 and an exact permission
// check there tests nothing. finalizeFile still runs on Windows — Chmod just
// has little to say about the result.
func TestFinalizeFileSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(path, []byte("DB"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeFile(path, 0o644); err != nil {
		t.Fatalf("finalizeFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode().Perm())
	}
}
