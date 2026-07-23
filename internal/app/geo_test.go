package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory belonging to a subscription that is no longer stored is dropped;
// the stored ones and the subscription being opened stay.
func TestPruneGeoDirs(t *testing.T) {
	// subscriptions.txt is resolved relative to the working directory.
	t.Chdir(t.TempDir())
	kept := "https://panel.example/kept"
	opened := "https://panel.example/opened" // not in the file, e.g. from the env
	if err := os.WriteFile("subscriptions.txt", []byte(kept+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "geo")
	dirs := []string{subKey(kept), subKey(opened), subKey("https://panel.example/gone")}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	pruneGeoDirs(root, opened)

	for i, d := range dirs {
		_, err := os.Stat(filepath.Join(root, d))
		if want := i < 2; want != (err == nil) {
			t.Errorf("%s: exists=%v, want %v", d, err == nil, want)
		}
	}
}
