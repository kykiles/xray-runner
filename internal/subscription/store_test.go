package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSubscriptions_FileNotExist(t *testing.T) {
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "nonexistent.txt")
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions on missing file: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected empty slice, got %d items", len(subs))
	}
}

func TestSaveAndLoadSubscription(t *testing.T) {
	dir := t.TempDir()
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(dir, "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	url := "https://example.com/sub"
	if err := SaveSubscription(url); err != nil {
		t.Fatalf("SaveSubscription: %v", err)
	}

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}
	if subs[0].URL != url {
		t.Errorf("URL = %q, want %q", subs[0].URL, url)
	}
	if subs[0].Name == "" {
		t.Error("expected non-empty Name")
	}
}

func TestLoadSubscriptions_WithComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "# My VPN\nhttps://vpn.example.com/sub\n\n# Backup\nhttps://backup.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "My VPN" {
		t.Errorf("Name[0] = %q, want %q", subs[0].Name, "My VPN")
	}
	if subs[1].Name != "Backup" {
		t.Errorf("Name[1] = %q, want %q", subs[1].Name, "Backup")
	}
}

func TestLoadSubscriptions_WithoutComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "https://vpn.example.com/sub\nhttps://other.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "vpn.example.com" {
		t.Errorf("Name[0] = %q, want hostname", subs[0].Name)
	}
}

// Guards the rewrite of RemoveSubscription: the entry goes away, the rest
// survive, and no scratch file is left in the directory.
func TestRemoveSubscription_RewritesRemainingEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "# A\nhttps://a.example.com/sub\n# B\nhttps://b.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	if err := RemoveSubscription(0); err != nil {
		t.Fatalf("RemoveSubscription: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only subscriptions.txt, got %v", names)
	}

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].URL != "https://b.example.com/sub" {
		t.Errorf("after removing index 0, got %+v", subs)
	}
}
