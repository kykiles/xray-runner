package tui

import (
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
