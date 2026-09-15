package app

import (
	"os"
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// Saving lands under <configs>/<subscription>/<name>.json, recreates the
// directory if it is gone, and overwrites an earlier copy of the same server.
// The configs root follows config.Path, so with no ./configs in the working
// directory it is the data dir.
func TestSaveConfig(t *testing.T) {
	isolateState(t)

	a := &App{}
	a.nav.subs = []subscription.NamedSubscription{{URL: "https://panel.example.com/sub/uuid", Name: "panel.example.com"}}

	path, err := a.saveConfig("DE · Frankfurt", `{"a":1}`)
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	root := config.Path("configs")
	want := filepath.Join(root, "panel.example.com", "DE___Frankfurt.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	os.RemoveAll(root)
	if _, err := a.saveConfig("DE · Frankfurt", `{"a":2}`); err != nil {
		t.Fatalf("saveConfig after the directory was deleted: %v", err)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "{\"a\":2}\n" {
		t.Errorf("content = %q, want the newer version", data)
	}
}

// In TUN mode the config viewer saves as root, and what it saves is handed to
// the sudo user (A04). recordHandBack swaps that handback for a record of what
// it reached, by file identity, so a test sees it without being root.
func recordHandBack(t *testing.T) *[]os.FileInfo {
	t.Helper()
	orig := handBack
	var got []os.FileInfo
	handBack = func(f *os.File) error {
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		got = append(got, fi)
		return nil
	}
	t.Cleanup(func() { handBack = orig })
	return &got
}

// handedBack reports whether the file at path itself — a link there is not
// followed — is among the ones handed back.
func handedBack(t *testing.T, got []os.FileInfo, path string) bool {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	for _, g := range got {
		if os.SameFile(g, fi) {
			return true
		}
	}
	return false
}

// plantSymlink is plantLink for a symlink that must really be there: wine
// reports success for os.Symlink without making one.
func plantSymlink(t *testing.T, target, path string) {
	t.Helper()
	plantLink(t, "symlink", target, path)
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skip("symlink reported made but not there")
	}
}

func newSaveApp() *App {
	a := &App{}
	a.nav.subs = []subscription.NamedSubscription{{URL: "https://panel.example.com/sub/uuid", Name: "panel.example.com"}}
	return a
}

// Under sudo everything the save creates changes hands: the configs dir, the
// subscription's dir, the file. Otherwise a configs dir made by root stays
// closed to the user until the session ends.
func TestSaveConfig_HandsBackWhatItCreated(t *testing.T) {
	isolateState(t)
	got := recordHandBack(t)

	path, err := newSaveApp().saveConfig("DE", `{"a":1}`)
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	dir := filepath.Dir(path)
	for _, p := range []string{filepath.Dir(dir), dir, path} {
		if !handedBack(t, *got, p) {
			t.Errorf("%s was not handed back", p)
		}
	}
	if len(*got) != 3 {
		t.Errorf("handed back %d files, want 3", len(*got))
	}
}

// Directories that were already there keep their owner: the save hands over
// only what it made.
func TestSaveConfig_ExistingDirsKeepTheirOwner(t *testing.T) {
	isolateState(t)
	dir := filepath.Join(config.Path(configsDir), "panel.example.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got := recordHandBack(t)

	path, err := newSaveApp().saveConfig("DE", `{"a":1}`)
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if len(*got) != 1 || !handedBack(t, *got, path) {
		t.Errorf("handed back %d files, want the saved file alone", len(*got))
	}
}

// A link planted in advance in place of the configs dir or the subscription's
// dir used to be followed: the file was written wherever it led, and that
// directory — say /etc — was handed to the user. Now the save refuses and the
// target is left alone.
func TestSaveConfig_PlantedDirLinkIsRefused(t *testing.T) {
	for _, c := range []struct{ name, link string }{
		{"configs", ""},
		{"subscription", "panel.example.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolateState(t)
			victim := t.TempDir()
			link := config.Path(configsDir)
			if c.link != "" {
				if err := os.MkdirAll(link, 0o700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, c.link)
			}
			plantSymlink(t, victim, link)
			got := recordHandBack(t)

			if _, err := newSaveApp().saveConfig("DE", `{"a":1}`); err == nil {
				t.Error("saveConfig followed a planted directory link")
			}
			if handedBack(t, *got, victim) {
				t.Error("the link target was handed back")
			}
			assertDirHolds(t, victim)
		})
	}
}

// The file is handed over before it gets its name. Handing it over by name
// afterwards reaches whatever sits at that name by then: another process that
// swaps the fresh file for a link right after the rename would get the link's
// target.
func TestSaveConfig_HandsOverTheFileNotTheName(t *testing.T) {
	isolateState(t)
	scratch := t.TempDir()
	plantSymlink(t, scratch, filepath.Join(scratch, "probe"))
	victim := filepath.Join(scratch, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := recordHandBack(t)
	orig := publishStaged
	publishStaged = func(r *os.Root, tmp, name string) error {
		if err := orig(r, tmp, name); err != nil {
			return err
		}
		if err := r.Remove(name); err != nil {
			return err
		}
		return os.Symlink(victim, filepath.Join(r.Name(), name))
	}
	t.Cleanup(func() { publishStaged = orig })

	if _, err := newSaveApp().saveConfig("DE", `{"a":1}`); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	if handedBack(t, *got, victim) {
		t.Error("the handback followed the name to what replaced the saved file")
	}
}

// A name full of separators must stay one path element.
func TestSafeName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"../../etc/passwd", "etc_passwd"},
		{"Нидерланды 🇳🇱", "Нидерланды"},
		{"", "config"},
		{"///", "config"},
	} {
		if got := safeName(c.in); got != c.want {
			t.Errorf("safeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
