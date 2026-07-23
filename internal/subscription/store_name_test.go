package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameSubscription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	old := subscriptionsFile
	subscriptionsFile = path
	t.Cleanup(func() { subscriptionsFile = old })

	const named, plain = "https://panel.example/named", "https://panel.example/plain"
	if err := os.WriteFile(path, []byte("# моё название\n"+named+"\n"+plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A name the user gave stays; a subscription without one takes the panel's.
	if err := NameSubscription(named, "панель"); err != nil {
		t.Fatal(err)
	}
	if err := NameSubscription(plain, "🤝 alohavpnbot"); err != nil {
		t.Fatal(err)
	}

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("subs = %v", subs)
	}
	if subs[0].Name != "моё название" {
		t.Errorf("named = %q, want the user's own", subs[0].Name)
	}
	if subs[1].Name != "🤝 alohavpnbot" {
		t.Errorf("plain = %q", subs[1].Name)
	}
}
