package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/system"
)

func newAppsModel(procs []system.Process, selected []string) appsModel {
	on := map[string]bool{}
	for _, s := range selected {
		on[strings.ToLower(s)] = true
	}
	rows := make([]appRow, 0, len(procs))
	running := map[string]bool{}
	for _, p := range procs {
		running[strings.ToLower(p.Name)] = true
		rows = append(rows, appRow{name: p.Name, pids: p.PIDs, on: on[strings.ToLower(p.Name)]})
	}
	for _, s := range selected {
		if !running[strings.ToLower(s)] {
			rows = append(rows, appRow{name: s, on: true})
		}
	}
	return appsModel{rows: rows, filter: newFilter()}
}

func pressApps(m appsModel, keys ...string) appsModel {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "/":
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.Update(msg)
		m = next.(appsModel)
	}
	return m
}

// A saved app that is not running has to stay on screen and stay untickable-off:
// dropping it would make apps.txt entries permanent once their process exits.
func TestAppsPicker_KeepsSelectedButNotRunning(t *testing.T) {
	m := newAppsModel([]system.Process{{Name: "code", PIDs: 3}}, []string{"code", "telegram-desktop"})

	if len(m.rows) != 2 {
		t.Fatalf("rows = %+v, want the running one plus the absent one", m.rows)
	}
	if got := m.chosen(); len(got) != 2 {
		t.Fatalf("chosen = %v, want both pre-ticked", got)
	}
	if !strings.Contains(m.View(), "не запущен") {
		t.Error("view does not mark the absent process")
	}

	// Cursor onto the absent row, untick it.
	m = pressApps(m, "down", " ")
	got := m.chosen()
	if len(got) != 1 || got[0] != "code" {
		t.Errorf("chosen = %v, want just [code] after unticking the absent one", got)
	}
}

// The filter narrows what is shown, never what is saved: a ticked row hidden by
// a query must survive.
func TestAppsPicker_FilterDoesNotDropSelection(t *testing.T) {
	m := newAppsModel([]system.Process{{Name: "code", PIDs: 1}, {Name: "sshd", PIDs: 2}}, nil)

	m = pressApps(m, " ") // tick code
	m = pressApps(m, "/")
	m = pressApps(m, "sshd")

	if vis := m.visible(); len(vis) != 1 || m.rows[vis[0]].name != "sshd" {
		t.Fatalf("visible = %v, want only sshd", vis)
	}
	got := m.chosen()
	if len(got) != 1 || got[0] != "code" {
		t.Errorf("chosen = %v, want [code] — the filter must not clear it", got)
	}
}

// Space toggles the row under the cursor in the *filtered* list, not the row at
// that index in the full list.
func TestAppsPicker_SpaceTogglesTheVisibleRow(t *testing.T) {
	m := newAppsModel([]system.Process{{Name: "code", PIDs: 1}, {Name: "sshd", PIDs: 2}}, nil)

	m = pressApps(m, "/")
	m = pressApps(m, "sshd")
	m = pressApps(m, "enter") // leave the filter input, query stays
	m = pressApps(m, " ")

	got := m.chosen()
	if len(got) != 1 || got[0] != "sshd" {
		t.Errorf("chosen = %v, want [sshd] — space hit the wrong row", got)
	}
}

// "c" is the way back to plain PROXY without unticking each app by hand. It
// clears every row, including the ones a filter is currently hiding.
func TestAppsPicker_ClearAllIgnoresFilter(t *testing.T) {
	m := newAppsModel([]system.Process{{Name: "code", PIDs: 3}, {Name: "chrome", PIDs: 37}}, []string{"code", "chrome"})

	// Filter down to one row, then clear.
	m = pressApps(m, "/", "chrome")
	if len(m.visible()) != 1 {
		t.Fatalf("filter should leave one row, got %d", len(m.visible()))
	}
	m = pressApps(m, "enter", "c") // leave the filter input, query stays

	if got := m.chosen(); len(got) != 0 {
		t.Fatalf("chosen = %v, want nothing ticked after clear", got)
	}
}
