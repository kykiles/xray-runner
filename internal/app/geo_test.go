package app

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/xraycfg"
)

// A directory belonging to a subscription that is no longer stored is dropped;
// the stored ones and the subscription being opened stay.
func TestPruneGeoDirs(t *testing.T) {
	// subscriptions.txt is resolved relative to the working directory.
	isolateState(t)
	kept := "https://panel.example/kept"
	opened := "https://panel.example/opened" // not in the file, e.g. from the env
	if err := os.WriteFile("subscriptions.txt", []byte(kept+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "geo")
	dirs := []string{subKey(kept), subKey(opened), subKey("https://panel.example/gone")}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	pruneGeoDirs(root, opened)

	for i, d := range dirs {
		_, err := os.Stat(filepath.Join(root, d))
		if want := i < 2; want != (err == nil) {
			t.Errorf("%s: exists=%v, want %v", d, err == nil, want)
		}
	}
}

// E03: an update of the panel's databases that fails leaves the previous pair
// where it was and as old as it was, so the next open tries again. That pair is
// what the panel's rules were written against: it stays in use, and the screen
// says the update failed. When it lacks a list the rules name — or there is no
// previous pair and the core's own databases lack it — the config is refused
// with the list and the failed update named. It used to switch to the core's
// databases whatever was installed.
func TestUseGeoAssetsAfterFailedUpdate(t *testing.T) {
	cases := []struct {
		name     string
		previous []string // geosite lists of the previous pair; nil — none installed
		panelDir bool     // the previous pair is in use, not the core's
		refused  bool
		note     string
	}{
		{"previous pair carries the lists", []string{"google", "torrent"}, true, false, "не обновились"},
		{"previous pair lacks a list", []string{"google"}, true, true, "не обновились"},
		{"no previous pair", nil, false, true, "не скачаны"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, panelDir := newGeoApp(t)
			stale := time.Now().Add(-2 * panelGeoTTL).Truncate(time.Second)
			if c.previous != nil {
				writeGeoPair(t, panelDir, c.previous, stale)
			}
			before := readGeoPair(t, panelDir)

			a.useGeoAssets(geoSubURL, unreachableGeo(t))

			want := filepath.Dir(a.binary)
			if c.panelDir {
				want = panelDir
			}
			if got := os.Getenv("XRAY_LOCATION_ASSET"); got != want {
				t.Errorf("xray reads databases from %s, want %s", got, want)
			}

			_, _, err := a.buildSessionConfig(&target{profileName: "Auto", profileRaw: torrentProfile(t)})
			switch {
			case c.refused && (err == nil || !strings.Contains(err.Error(), "torrent") || !strings.Contains(err.Error(), c.note)):
				t.Errorf("err = %v, want a refusal naming geosite:torrent and saying %q", err, c.note)
			case !c.refused && err != nil:
				t.Errorf("err = %v, want the config built on the previous pair", err)
			}

			a.pendingNote = "Прошлый режим TUN недоступен."
			a.noteGeoDatabases()
			for _, s := range []string{"Прошлый режим TUN недоступен.", c.note} {
				if !strings.Contains(a.pendingNote, s) {
					t.Errorf("note %q lacks %q", a.pendingNote, s)
				}
			}

			// Nothing that marks the pair installed moved: the same files, the
			// same age, and no leftovers of the attempt next to them.
			if after := readGeoPair(t, panelDir); !bytes.Equal(after.content, before.content) || !after.mtime.Equal(before.mtime) || after.names != before.names {
				t.Errorf("panel dir changed by the failed update: %+v → %+v", before, after)
			}
			if c.previous != nil && !readGeoPair(t, panelDir).mtime.Equal(stale) {
				t.Errorf("the previous pair looks updated")
			}
		})
	}
}

// The note lasts as long as the failure: an open that finds the panel's pair
// current again clears it, from the screen and from a refusal alike.
func TestUseGeoAssetsNoteClearsWhenCurrent(t *testing.T) {
	a, panelDir := newGeoApp(t)
	writeGeoPair(t, panelDir, []string{"google"}, time.Now().Add(-2*panelGeoTTL))
	a.useGeoAssets(geoSubURL, unreachableGeo(t))
	if a.geoNote == "" {
		t.Fatal("no note after a failed update")
	}

	writeGeoPair(t, panelDir, []string{"google"}, time.Now())
	a.useGeoAssets(geoSubURL, unreachableGeo(t))

	a.pendingNote = ""
	a.noteGeoDatabases()
	if a.pendingNote != "" {
		t.Errorf("note %q with the panel's pair current", a.pendingNote)
	}
	_, _, err := a.buildSessionConfig(&target{profileName: "Auto", profileRaw: torrentProfile(t)})
	if err == nil || strings.Contains(err.Error(), "не обновились") {
		t.Errorf("err = %v, want a refusal naming the list and no failed update", err)
	}
}

const geoSubURL = "https://panel.example/sub"

// newGeoApp is an app whose core sits in a temp dir with databases lacking
// geosite:torrent, and the dir the panel's pair for geoSubURL goes in.
func newGeoApp(t *testing.T) (*App, string) {
	t.Helper()
	isolateState(t) // pruneGeoDirs reads the saved subscriptions
	t.Setenv("XRAY_LOCATION_ASSET", "")
	t.Cleanup(func() { xraycfg.SetGeoAssets("", "") })

	a := newTemplateApp(t)
	coreDir := t.TempDir()
	a.binary = filepath.Join(coreDir, "xray")
	writeGeoPair(t, coreDir, []string{"google"}, time.Now())
	return a, filepath.Join(coreDir, "geo", subKey(geoSubURL))
}

// unreachableGeo points both databases at a loopback port nobody listens on, so
// the download fails at once.
func unreachableGeo(t *testing.T) subscription.PanelInfo {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return subscription.PanelInfo{SiteURL: "https://" + addr + "/geosite.dat", IPURL: "https://" + addr + "/geoip.dat"}
}

// writeGeoPair installs geosite.dat with the given lists and geoip.dat with
// private in dir, both dated mtime.
func writeGeoPair(t *testing.T, dir string, sites []string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for file, names := range map[string][]string{"geosite.dat": sites, "geoip.dat": {"private"}} {
		// One entry of field 1 per list, whose own field 1 is the list name — the
		// shape of both real databases.
		var dat []byte
		for _, n := range names {
			entry := append([]byte{0x0A, byte(len(n))}, n...)
			dat = append(append(dat, 0x0A, byte(len(entry))), entry...)
		}
		path := filepath.Join(dir, file)
		if err := os.WriteFile(path, dat, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// geoPairState is what a failed update must leave as it found: the directory
// listing, both files' bytes and geosite.dat's age.
type geoPairState struct {
	names   string
	content []byte
	mtime   time.Time
}

func readGeoPair(t *testing.T, dir string) geoPairState {
	t.Helper()
	var s geoPairState
	entries, _ := os.ReadDir(dir) // absent before the first install
	for _, e := range entries {
		s.names += e.Name() + " "
	}
	for _, f := range []string{"geoip.dat", "geosite.dat"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		s.content = append(s.content, b...)
	}
	if fi, err := os.Stat(filepath.Join(dir, "geosite.dat")); err == nil {
		s.mtime = fi.ModTime()
	}
	return s
}
