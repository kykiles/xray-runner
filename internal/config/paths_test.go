package config

import (
	"os"
	"path/filepath"
	"testing"
)

// placeProgram makes the program live in a fresh directory of the test's, and
// returns it.
func placeProgram(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := Executable
	Executable = func() (string, error) { return filepath.Join(dir, "xray-runner"), nil }
	t.Cleanup(func() { Executable = orig })
	return dir
}

// A file already next to the program keeps being used; anything else moves to
// the data dir. Getting this backwards silently hides an existing subscription
// list.
func TestPath_PrefersExistingFileNextToProgram(t *testing.T) {
	prog := placeProgram(t)
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)

	if got, want := Path("subscriptions.txt"), filepath.Join(data, "xray-runner", "subscriptions.txt"); got != want {
		t.Fatalf("Path() = %q, want %q in the data dir when the file does not exist", got, want)
	}

	legacy := filepath.Join(prog, "subscriptions.txt")
	if err := os.WriteFile(legacy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Path("subscriptions.txt"); got != legacy {
		t.Errorf("Path() = %q, want the existing file next to the program %q", got, legacy)
	}
}

// Where the program was started from is not consulted: under sudo it is a
// directory of the user's choosing, and root would read and write there (H02).
func TestPath_IgnoresWorkingDir(t *testing.T) {
	placeProgram(t)
	t.Chdir(t.TempDir())
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)
	if err := os.WriteFile("subscriptions.txt", []byte("planted"), 0600); err != nil {
		t.Fatal(err)
	}

	if got, want := Path("subscriptions.txt"), filepath.Join(data, "xray-runner", "subscriptions.txt"); got != want {
		t.Errorf("Path() = %q, want %q — a file in the working directory must not win", got, want)
	}
}

// .env is read next to the program, not from the working directory (H02).
func TestEnvFile_NextToProgram(t *testing.T) {
	prog := placeProgram(t)
	t.Chdir(t.TempDir())
	if got, want := EnvFile(), filepath.Join(prog, ".env"); got != want {
		t.Errorf("EnvFile() = %q, want %q", got, want)
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
