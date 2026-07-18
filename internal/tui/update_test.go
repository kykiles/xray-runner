package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xray-runner/internal/updater"
)

func TestUpdateView_ShowsInstalledCore(t *testing.T) {
	m := updateModel{installed: "26.6.27", stage: updMenu, width: 120}
	if out := m.View(); !strings.Contains(out, "Текущее ядро: 26.6.27") {
		t.Errorf("header should show the installed core version, got:\n%s", out)
	}

	m.installed = ""
	if out := m.View(); !strings.Contains(out, "Текущее ядро: неизвестно") {
		t.Errorf("header should read 'неизвестно' when the version is unknown, got:\n%s", out)
	}
}

// After a successful core install the header must reflect the version we just
// installed, not the one captured when the screen opened.
func TestUpdate_RefreshesCoreAfterInstall(t *testing.T) {
	m := updateModel{installed: "26.6.27", kind: kindCore, pendingTag: "v26.6.30", stage: updWorking}
	next, _ := m.Update(installedMsg{})
	um := next.(updateModel)
	if um.stage != updDone {
		t.Fatalf("stage should be updDone, got %v", um.stage)
	}
	if um.installed != "26.6.30" {
		t.Errorf("installed core should refresh to 26.6.30, got %q", um.installed)
	}
}

// A failed install must leave the header untouched.
func TestUpdate_KeepsHeaderOnInstallError(t *testing.T) {
	m := updateModel{installed: "26.6.27", kind: kindCore, pendingTag: "v26.6.30", stage: updWorking}
	next, _ := m.Update(installedMsg{err: os.ErrPermission})
	um := next.(updateModel)
	if um.installed != "26.6.27" {
		t.Errorf("installed core must not change on error, got %q", um.installed)
	}
}

// After a successful geo install the header must re-read geoip.dat's date.
func TestUpdate_RefreshesGeoAfterInstall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip.dat"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := updateModel{dir: dir, geoDate: "", kind: kindGeo, stage: updWorking}
	next, _ := m.Update(installedMsg{})
	um := next.(updateModel)
	if um.geoDate == "" {
		t.Errorf("geoDate should refresh after a geo install, got empty")
	}
}

func TestUpdateView_MarksInstalledRelease(t *testing.T) {
	m := updateModel{
		installed: "26.6.27",
		stage:     updReleases,
		width:     120,
		releases: []updater.Release{
			{Tag: "v26.6.30"},
			{Tag: "v26.6.27"},
		},
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	var v27, v30 string
	for _, l := range lines {
		if strings.Contains(l, "v26.6.27") {
			v27 = l
		}
		if strings.Contains(l, "v26.6.30") {
			v30 = l
		}
	}
	if !strings.Contains(v27, "(установлено)") {
		t.Errorf("the matching release line must be marked (установлено), got: %q", v27)
	}
	if strings.Contains(v30, "(установлено)") {
		t.Errorf("a non-matching release must not be marked, got: %q", v30)
	}
}
