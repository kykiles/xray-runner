package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// M-2: Ctrl+C during bring-up must cancel the session context, but it must NOT
// close the screen right away — bringUp is still editing the routing table, and
// quitting early would let teardown race an in-flight bring-up.
func TestConnecting_CtrlCCancelsAndWaits(t *testing.T) {
	canceled := false
	m := connectingModel{title: "srv", cancel: func() { canceled = true }}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	cm := got.(connectingModel)

	if !canceled {
		t.Error("Ctrl+C did not cancel the session context")
	}
	if !cm.canceling {
		t.Error("model did not enter the canceling state")
	}
	if cmd != nil {
		t.Error("screen quit before connect returned; teardown would race bring-up")
	}
}

// Once the canceled bring-up finally returns, the screen closes and reports the
// cancellation — whatever connect itself returned. A Ctrl+C landing exactly as
// connect succeeded still means "cancel", and teardown must run.
func TestConnecting_ReportsCancellationOverConnectResult(t *testing.T) {
	for _, connectErr := range []error{nil, errors.New("TUN не поднялся")} {
		m := connectingModel{canceling: true}
		got, cmd := m.Update(connectDoneMsg{err: connectErr})
		if cmd == nil {
			t.Fatal("expected the screen to quit once connect returned")
		}
		if err := got.(connectingModel).err; !errors.Is(err, ErrConnectCanceled) {
			t.Errorf("connect err %v → %v, want ErrConnectCanceled", connectErr, err)
		}
	}
}

// Without a Ctrl+C the screen keeps its old behaviour: connect's result passes
// straight through.
func TestConnecting_PassesConnectErrorThrough(t *testing.T) {
	want := errors.New("TUN не поднялся")
	m := connectingModel{}
	got, cmd := m.Update(connectDoneMsg{err: want})
	if cmd == nil {
		t.Fatal("expected quit")
	}
	if err := got.(connectingModel).err; !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}
