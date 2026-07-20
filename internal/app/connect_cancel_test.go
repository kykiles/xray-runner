package app

import (
	"errors"
	"testing"

	"xray-runner/internal/subscription"
	"xray-runner/internal/tui"
)

func connectTarget() *target {
	return &target{entry: &subscription.SubEntry{
		Protocol: "vless", Address: "203.0.113.5", Port: 443, Remarks: "srv",
	}}
}

// M-2: the bring-up screen needs a way to cancel the session context, otherwise
// Ctrl+C during bring-up either does nothing or closes the screen while the
// routing table is still being edited.
func TestShowConnecting_PassesCancelToScreen(t *testing.T) {
	var gotCancel func()
	a := &App{}
	a.connectScreen = func(_ string, connect func() error, cancel func()) error {
		gotCancel = cancel
		return connect()
	}

	if err := a.showConnecting(connectTarget(), func() error { return nil }, func() {}); err != nil {
		t.Fatalf("showConnecting: %v", err)
	}
	if gotCancel == nil {
		t.Fatal("screen got no cancel func, so Ctrl+C cannot abort the bring-up")
	}
}

// A canceled bring-up must surface as ErrConnectCanceled so runSession can tear
// the half-built session down and quit the app.
func TestShowConnecting_SurfacesCancellation(t *testing.T) {
	a := &App{}
	a.connectScreen = func(string, func() error, func()) error { return tui.ErrConnectCanceled }

	err := a.showConnecting(connectTarget(), func() error { return nil }, func() {})
	if !errors.Is(err, tui.ErrConnectCanceled) {
		t.Fatalf("err = %v, want ErrConnectCanceled", err)
	}
}

// A headless run has no screen and therefore nothing to cancel from: bring-up
// runs straight through, as before.
func TestShowConnecting_HeadlessSkipsScreen(t *testing.T) {
	a := &App{noTTY: true}
	a.connectScreen = func(string, func() error, func()) error {
		t.Fatal("headless run must not open the connecting screen")
		return nil
	}

	ran := false
	if err := a.showConnecting(connectTarget(), func() error { ran = true; return nil }, func() {}); err != nil {
		t.Fatalf("showConnecting: %v", err)
	}
	if !ran {
		t.Error("connect did not run on the headless path")
	}
}
