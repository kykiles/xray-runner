package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A file already next to the binary keeps being used; anything else moves to the
// data dir. Getting this backwards silently hides an existing subscription list.
func TestPathPrefersExistingFileInWorkingDir(t *testing.T) {
	t.Chdir(t.TempDir())
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)

	if got := Path("subscriptions.txt"); got == "subscriptions.txt" {
		t.Fatalf("Path() = %q, want a path in the data dir when the file does not exist", got)
	}

	if err := os.WriteFile("subscriptions.txt", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Path("subscriptions.txt"); got != "subscriptions.txt" {
		t.Errorf("Path() = %q, want the existing file in the working dir", got)
	}
}

func TestDataDirIsCreated(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)

	dir := DataDir()
	if dir == "" {
		t.Fatal("DataDir() = \"\", want a path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
	if want := filepath.Join(data, "xray-runner"); dir != want {
		t.Errorf("DataDir() = %q, want %q", dir, want)
	}
}
