package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// A04: the config path is predictable, so a link can be waiting there before
// the tool — possibly running as root — writes the config. The write replaces
// the name itself: whatever sits on the other end of a link keeps its bytes.

// prettyConfig is what writeConfigJSON stores for `{"log":{}}`.
const prettyConfig = "{\n  \"log\": {}\n}"

func newConfigApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	return &App{tmpFile: filepath.Join(dir, "xray_config.json")}, dir
}

// plantLink makes path a link of the given kind to victim, skipping where the
// system refuses (a symlink on Windows needs developer mode or admin rights).
func plantLink(t *testing.T, kind, victim, path string) {
	t.Helper()
	link := os.Link
	if kind == "symlink" {
		link = os.Symlink
	}
	if err := link(victim, path); err != nil {
		t.Skipf("%s: %v", kind, err)
	}
}

// assertReplaced fails unless path is now a regular file of its own holding
// want, while victim still holds its original bytes.
func assertReplaced(t *testing.T, path, want, victim string) {
	t.Helper()
	assertFileHolds(t, victim, "original")
	assertFileHolds(t, path, want)
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is still %v, want a regular file", filepath.Base(path), fi.Mode().Type())
	}
	vi, err := os.Stat(victim)
	if err != nil {
		t.Fatalf("stat victim: %v", err)
	}
	if os.SameFile(fi, vi) {
		t.Errorf("%s still shares its file with the victim", filepath.Base(path))
	}
}

// assertDirHolds fails unless dir holds exactly names: a temp left behind would
// still carry the config's secrets.
func assertDirHolds(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(names)
	if !slices.Equal(got, names) {
		t.Errorf("dir holds %q, want %q", got, names)
	}
}

func TestWriteConfigJSON_LinkedNameIsReplaced(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			a, dir := newConfigApp(t)
			victim := filepath.Join(dir, "victim")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			plantLink(t, kind, victim, a.tmpFile)

			if err := a.writeConfigJSON([]byte(`{"log":{}}`)); err != nil {
				t.Fatalf("writeConfigJSON: %v", err)
			}
			assertReplaced(t, a.tmpFile, prettyConfig, victim)
			assertDirHolds(t, dir, "victim", "xray_config.json")
		})
	}
}

// 0600 used to apply only to a file WriteFile created; an older config left
// readable by everyone kept its mode and got the new secrets written into it.
func TestWriteConfigJSON_LooseModeBecomesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not Windows access control")
	}
	a, _ := newConfigApp(t)
	if err := os.WriteFile(a.tmpFile, []byte("old-config"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.tmpFile, 0o644); err != nil { // past the umask
		t.Fatal(err)
	}

	if err := a.writeConfigJSON([]byte(`{"log":{}}`)); err != nil {
		t.Fatalf("writeConfigJSON: %v", err)
	}
	fi, err := os.Stat(a.tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %v, want 0600", perm)
	}
}

func TestWriteConfigJSON_InvalidJSONChangesNothing(t *testing.T) {
	a, dir := newConfigApp(t)
	if err := os.WriteFile(a.tmpFile, []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.writeConfigJSON([]byte(`{"log":`)); err == nil {
		t.Fatal("writeConfigJSON accepted broken JSON")
	}
	assertFileHolds(t, a.tmpFile, "old-config")
	assertDirHolds(t, dir, "xray_config.json")
}

// A write that fails at any step leaves the previous config in place — the
// running core rereads it on its next restart — and takes its temp with it.
func TestWriteConfigJSON_FailedStepKeepsOldFile(t *testing.T) {
	injected := errors.New("injected failure")
	for _, c := range []struct {
		step string
		fail func()
	}{
		{"write", func() { stagedWrite = func(*os.File, []byte) (int, error) { return 0, injected } }},
		{"sync", func() { stagedSync = func(*os.File) error { return injected } }},
		// The handle stays open: cleanup has to close it before the temp goes,
		// or Windows refuses to delete it.
		{"close", func() { stagedClose = func(*os.File) error { return injected } }},
		{"publish", func() { publishStaged = func(string, string) error { return injected } }},
	} {
		t.Run(c.step, func(t *testing.T) {
			w, s, cl, p := stagedWrite, stagedSync, stagedClose, publishStaged
			t.Cleanup(func() { stagedWrite, stagedSync, stagedClose, publishStaged = w, s, cl, p })
			c.fail()

			a, dir := newConfigApp(t)
			if err := os.WriteFile(a.tmpFile, []byte("old-config"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := a.writeConfigJSON([]byte(`{"log":{}}`))
			if !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the injected %s failure", err, c.step)
			}
			assertFileHolds(t, a.tmpFile, "old-config")
			assertDirHolds(t, dir, "xray_config.json")
		})
	}
}

// The config viewer saves under ./configs when that directory exists, and in
// TUN mode it does so as root: a link planted at the file name is replaced too.
func TestSaveConfig_LinkedNameIsReplaced(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			isolateState(t)
			a := &App{}
			a.nav.subs = []subscription.NamedSubscription{{URL: "https://panel.example.com/sub/uuid", Name: "panel.example.com"}}
			dir := filepath.Join(config.Path(configsDir), "panel.example.com")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// Same volume as dir, or Windows refuses the hard link.
			victim := filepath.Join(config.Path(configsDir), "victim")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "DE.json")
			plantLink(t, kind, victim, path)

			if _, err := a.saveConfig("DE", `{"a":1}`); err != nil {
				t.Fatalf("saveConfig: %v", err)
			}
			assertReplaced(t, path, "{\"a\":1}\n", victim)
			assertDirHolds(t, dir, "DE.json")
		})
	}
}
