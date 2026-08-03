package app

// The status screen is opened once per attempt: on the first entry and again
// after a refused mode switch. Both entries must behave the same way, which is
// why they go through one function.

import (
	"context"
	"errors"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/tui"
)

func statusTarget() *target {
	return &target{entry: &subscription.SubEntry{
		Protocol: "vless", Address: "203.0.113.5", Port: 443, Remarks: "srv",
	}}
}

// A dead core (or Ctrl+C) closes the update channel, which is what takes the
// screen down. Without it the user keeps staring at a session that is gone.
func TestStatusScreenClosesWhenSessionEnds(t *testing.T) {
	closed := make(chan struct{})
	a := &App{mode: "proxy"}
	a.showStatus = func(_ tui.StatusInfo, ch <-chan tui.StatusUpdate) (tui.StatusAction, error) {
		for range ch { // drain until closed
		}
		close(closed)
		return tui.StatusQuit, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if _, err := a.showStatusScreen(ctx, statusTarget(), sessionPorts{socks: 1, http: 2}, func() {}, tui.StatusUpdate{}); err != nil {
		t.Fatalf("showStatusScreen: %v", err)
	}

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("update channel was never closed after the session context ended")
	}
}

// A refused switch keeps the session alive and re-opens the screen with the
// refusal on it.
func TestWatchSessionSurvivesRefusedModeSwitch(t *testing.T) {
	isolateState(t)

	refusal := errors.New("режим TUN требует прав root")
	var notes []string
	screens := 0

	a := &App{
		mode:            "proxy",
		checkPrivileges: func() error { return refusal },
	}
	a.showStatus = func(_ tui.StatusInfo, ch <-chan tui.StatusUpdate) (tui.StatusAction, error) {
		screens++
		select {
		case u := <-ch:
			notes = append(notes, u.Note)
		default:
		}
		if screens == 1 {
			return tui.StatusSwitchMode, nil // refused: TUN needs root
		}
		return tui.StatusBack, nil
	}

	action, err := a.watchSession(context.Background(), statusTarget(), sessionPorts{socks: 1, http: 2}, func() {})
	if err != nil {
		t.Fatalf("watchSession: %v", err)
	}
	if action != tui.StatusBack {
		t.Errorf("action = %v, want %v", action, tui.StatusBack)
	}
	if screens != 2 {
		t.Fatalf("status screen shown %d times, want 2 (the refusal must not end the session)", screens)
	}
	if len(notes) != 1 || notes[0] != "⚠ "+refusal.Error() {
		t.Errorf("notes = %q, want the refusal on the second screen", notes)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q, want proxy — a refused switch changes nothing", a.mode)
	}
}
