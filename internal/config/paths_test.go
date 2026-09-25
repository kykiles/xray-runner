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

// isolateDirs points the data dir and the cache at fresh directories of the
// test's, on both systems' variables, and returns them.
func isolateDirs(t *testing.T) (data, cache string) {
	t.Helper()
	data, cache = t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)
	return data, cache
}

// onOS makes the Windows-only rules of this package apply, or not.
func onOS(t *testing.T, name string) {
	t.Helper()
	orig := goos
	goos = name
	t.Cleanup(func() { goos = orig })
}

// Downloaded databases go to the user's cache, one level at a time (H03).
func TestCacheSubdir(t *testing.T) {
	_, cache := isolateDirs(t)

	dir, err := CacheSubdir("geo", "abc")
	if err != nil {
		t.Fatalf("CacheSubdir: %v", err)
	}
	if want := filepath.Join(cache, "xray-runner", "geo", "abc"); dir != want {
		t.Errorf("CacheSubdir = %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("not created: %v", err)
	}
}

// What belongs to one machine stays out of Windows' Roaming profile, and on
// Linux out of the cache, which may be thrown away (H04).
func TestLocalDir(t *testing.T) {
	data, cache := isolateDirs(t)

	onOS(t, "windows")
	if got, want := LocalDir(), filepath.Join(cache, "xray-runner"); got != want {
		t.Errorf("Windows: LocalDir = %q, want %q", got, want)
	}
	onOS(t, "linux")
	if got, want := LocalDir(), filepath.Join(data, "xray-runner"); got != want {
		t.Errorf("Linux: LocalDir = %q, want %q", got, want)
	}
}

// The HWID earlier versions kept in Roaming moves to the local dir with its
// value: a new one would be a new device to the panel (H04).
func TestGetOrCreateHWID_MovesFromRoaming(t *testing.T) {
	placeProgram(t)
	data, cache := isolateDirs(t)
	onOS(t, "windows")
	old := filepath.Join(data, "xray-runner", "hwid")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("roaming-hwid"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := GetOrCreateHWID(""); got != "roaming-hwid" {
		t.Fatalf("HWID = %q, want the one from Roaming", got)
	}
	moved, err := os.ReadFile(filepath.Join(cache, "xray-runner", "hwid"))
	if err != nil || string(moved) != "roaming-hwid" {
		t.Errorf("HWID in the local dir = %q (%v), want it carried over", moved, err)
	}
	if got := GetOrCreateHWID(""); got != "roaming-hwid" {
		t.Errorf("second call HWID = %q, want the same", got)
	}
}
