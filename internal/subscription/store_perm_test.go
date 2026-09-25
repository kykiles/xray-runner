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

// Adding a subscription replaces a symlink planted in place of the list rather
// than appending to whatever it points at (G02).
func TestSaveSubscription_DoesNotWriteThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "subscriptions.txt")
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	// Reading the list through the link is refused, so nothing is added.
	if err := SaveSubscription("https://a.example.com/sub"); err == nil {
		t.Error("SaveSubscription went ahead over a symlinked list")
	}
	if got, _ := os.ReadFile(victim); string(got) != "must survive" {
		t.Errorf("symlink target was written through: %q", got)
	}
}
