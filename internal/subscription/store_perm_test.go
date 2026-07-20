//go:build !windows

package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

// The rewritten file must keep 0600: it holds subscription tokens. Windows has
// no POSIX permission bits, so this check only makes sense elsewhere.
func TestRemoveSubscription_KeepsOwnerOnlyPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "https://a.example.com/sub\nhttps://b.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	if err := RemoveSubscription(0); err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}
