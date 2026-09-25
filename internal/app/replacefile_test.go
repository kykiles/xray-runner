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

// A04: a file the tool writes under a predictable name — as root in TUN mode —
// may have a link waiting there. replaceInRoot replaces the name itself:
// whatever sits on the other end of a link keeps its bytes. saveConfig writes
// through it; these drive it directly, by path.

// replaceFile is replaceInRoot for a path, the way the tests drive it.
func replaceFile(path string, data []byte) error {
	r, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return replaceInRoot(r, filepath.Base(path), data, nil)
}

func newReplacePath(t *testing.T) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	return filepath.Join(dir, "config.json"), dir
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

func TestReplaceFile_LinkedNameIsReplaced(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path, dir := newReplacePath(t)
			victim := filepath.Join(dir, "victim")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			plantLink(t, kind, victim, path)

			if err := replaceFile(path, []byte("new")); err != nil {
				t.Fatalf("replaceFile: %v", err)
			}
			assertReplaced(t, path, "new", victim)
			assertDirHolds(t, dir, "victim", "config.json")
		})
	}
}

// 0600 used to apply only to a file WriteFile created; an older file left
// readable by everyone kept its mode and got the new secrets written into it.
func TestReplaceFile_LooseModeBecomesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not Windows access control")
	}
	path, _ := newReplacePath(t)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // past the umask
		t.Fatal(err)
	}

	if err := replaceFile(path, []byte("new")); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

// A write that fails at any step leaves the previous file in place and takes
// its temp with it.
func TestReplaceFile_FailedStepKeepsOldFile(t *testing.T) {
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
		{"publish", func() { publishStaged = func(*os.Root, string, string) error { return injected } }},
	} {
		t.Run(c.step, func(t *testing.T) {
			w, s, cl, p := stagedWrite, stagedSync, stagedClose, publishStaged
			t.Cleanup(func() { stagedWrite, stagedSync, stagedClose, publishStaged = w, s, cl, p })
			c.fail()

			path, dir := newReplacePath(t)
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := replaceFile(path, []byte("new")); !errors.Is(err, injected) {
				t.Fatalf("err = %v, want the injected %s failure", err, c.step)
			}
			assertFileHolds(t, path, "old")
			assertDirHolds(t, dir, "config.json")
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
