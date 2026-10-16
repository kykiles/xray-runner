package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"xray-runner/internal/updater"

	tea "github.com/charmbracelet/bubbletea"
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

// A refused geo install — a checksum that failed, say — must not mark the
// release installed, or the next → would skip it as up to date.
func TestUpdate_GeoInstallErrorLeavesReleaseUnmarked(t *testing.T) {
	const tag = "v-geo-install-error"
	m := updateModel{dir: t.TempDir(), kind: kindGeo, geoTag: tag, stage: updWorking}
	m.Update(installedMsg{err: os.ErrPermission})
	if updater.GeoInstalled(tag) {
		t.Error("a failed geo install marked the release installed")
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

// L-3: the release list rendered every entry regardless of terminal height, so
// on a short terminal the legend was pushed off the bottom. servers.go and
// profiles.go already window their lists; this screen must too.
func TestUpdateView_ReleaseListFitsTerminal(t *testing.T) {
	rels := make([]updater.Release, 20)
	for i := range rels {
		rels[i] = updater.Release{Tag: fmt.Sprintf("v26.6.%d", i)}
	}
	m := updateModel{stage: updReleases, releases: rels, width: 120, height: 14}

	out := m.View()
	if n := strings.Count(out, "\n"); n > 14 {
		t.Errorf("view is %d lines on a 14-line terminal:\n%s", n, out)
	}
	if !strings.Contains(out, "→ установить") {
		t.Errorf("legend pushed off the screen:\n%s", out)
	}
}

// A core that is not the program's own is not updated from here, and neither
// are the databases beside it: nothing is fetched, and the screen says why
// (H05).
func TestUpdate_ForeignCoreIsLeftAlone(t *testing.T) {
	for cursor, what := range updateMenu {
		t.Run(what, func(t *testing.T) {
			m := updateModel{foreign: true, dir: "/usr/local/bin", stage: updMenu, cursor: cursor}

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

			got := next.(updateModel)
			if cmd != nil {
				t.Error("a fetch was started for a core that is not the program's")
			}
			if got.stage != updDone || got.err == nil || !strings.Contains(got.err.Error(), "/usr/local/bin") {
				t.Errorf("stage %v, err %v; want done with the reason naming the core's directory", got.stage, got.err)
			}
		})
	}
}

// Once the guard is closed no operation starts, and closing waits for the ones
// that did.
func TestOpGuard_ClosingWaitsAndRefusesLaterStarts(t *testing.T) {
	g := &opGuard{}
	if !g.start() {
		t.Fatal("start() on an open guard returned false")
	}

	closed := make(chan struct{})
	go func() {
		g.closeAndWait()
		close(closed)
	}()

	// closeAndWait sets closed before it waits, so once start() refuses, the
	// close is under way and the started operation is still counted.
	for g.start() {
		g.done()
		runtime.Gosched()
	}
	select {
	case <-closed:
		t.Fatal("closeAndWait returned while an operation was still running")
	default:
	}

	g.done()
	<-closed
	if g.start() {
		t.Error("start() on a closed guard returned true")
	}
}

// A guard nobody started on closes at once.
func TestOpGuard_CloseWithoutOperations(t *testing.T) {
	g := &opGuard{}
	g.closeAndWait()
	if g.start() {
		t.Error("start() on a closed guard returned true")
	}
}

// An install command that runs after the screen was closed does nothing: it
// reports the cancellation and never reaches the installer.
func TestUpdate_InstallAfterCloseIsRefused(t *testing.T) {
	var called atomic.Int32
	origGeo, origCore := installReleaseGeo, installCoreBinary
	installReleaseGeo = func(context.Context, updater.Asset, updater.Asset, string) error {
		called.Add(1)
		return nil
	}
	installCoreBinary = func(context.Context, updater.Asset, string) error {
		called.Add(1)
		return nil
	}
	t.Cleanup(func() { installReleaseGeo, installCoreBinary = origGeo, origCore })

	ops := &opGuard{}
	m := updateModel{ctx: context.Background(), ops: ops, dir: t.TempDir(), xrayPath: "xray"}
	geoCmd := m.installGeo(updater.Asset{}, updater.Asset{})
	coreCmd := m.installCore(updater.Asset{})
	ops.closeAndWait()

	for name, cmd := range map[string]tea.Cmd{"geo": geoCmd, "core": coreCmd} {
		msg, ok := cmd().(installedMsg)
		if !ok || !errors.Is(msg.err, context.Canceled) {
			t.Errorf("%s: got %#v, want installedMsg with context.Canceled", name, msg)
		}
	}
	if n := called.Load(); n != 0 {
		t.Errorf("installer ran %d times after the screen was closed", n)
	}
}

// A running install holds closeAndWait until it returns, and the installer sees
// the context it was given.
func TestUpdate_CloseWaitsForRunningInstall(t *testing.T) {
	entered, finish, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	origGeo := installReleaseGeo
	installReleaseGeo = func(ctx context.Context, _, _ updater.Asset, _ string) error {
		defer close(returned)
		close(entered)
		<-finish
		return ctx.Err()
	}
	t.Cleanup(func() { installReleaseGeo = origGeo })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := &opGuard{}
	m := updateModel{ctx: ctx, ops: ops, dir: t.TempDir()}
	cmd := m.installGeo(updater.Asset{}, updater.Asset{})

	msgs := make(chan tea.Msg, 1)
	go func() { msgs <- cmd() }()
	<-entered

	cancel()
	closed := make(chan struct{})
	go func() {
		ops.closeAndWait()
		close(closed)
	}()

	close(finish)
	<-closed
	select {
	case <-returned:
	default:
		t.Fatal("closeAndWait returned before the install did")
	}
	if msg := (<-msgs).(installedMsg); !errors.Is(msg.err, context.Canceled) {
		t.Errorf("install err = %v, want context.Canceled", msg.err)
	}
}
