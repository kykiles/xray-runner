package app

import (
	"errors"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

func newModeApp(t *testing.T, envMode string, priv func() error) *App {
	t.Helper()
	isolateState(t)
	a := New(&config.Config{Mode: envMode}, Options{})
	if priv != nil {
		a.checkPrivileges = priv
	}
	return a
}

var errNoRoot = errors.New("режим TUN требует прав root")

// The mode the user switched to last time wins over MODE in .env, which only
// seeds the first run.
func TestResolveModeSavedWinsOverEnv(t *testing.T) {
	a := newModeApp(t, "proxy", func() error { return nil })
	if err := saveLastState(LastState{Mode: "tun"}); err != nil {
		t.Fatal(err)
	}

	if err := a.resolveMode(); err != nil {
		t.Fatalf("resolveMode() = %v, want nil", err)
	}
	if a.mode != "tun" {
		t.Errorf("mode = %q, want %q", a.mode, "tun")
	}
}

// Nothing saved yet: MODE from .env is all there is to go on.
func TestResolveModeFallsBackToEnvWhenNothingSaved(t *testing.T) {
	a := newModeApp(t, "proxy", nil)

	if err := a.resolveMode(); err != nil {
		t.Fatalf("resolveMode() = %v, want nil", err)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q, want %q", a.mode, "proxy")
	}
	if a.modeFromState {
		t.Error("modeFromState = true, want false with no saved state")
	}
}

// A remembered tun without root must not stop the app: it drops to proxy, says
// so, and leaves the saved preference alone so a sudo restart returns to tun.
func TestResolveModeSavedTunWithoutPrivilegesFallsBack(t *testing.T) {
	a := newModeApp(t, "proxy", func() error { return errNoRoot })
	if err := saveLastState(LastState{Mode: "tun"}); err != nil {
		t.Fatal(err)
	}

	if err := a.resolveMode(); err != nil {
		t.Fatalf("resolveMode() = %v, want nil (fallback, not failure)", err)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q, want %q", a.mode, "proxy")
	}
	if a.pendingNote == "" {
		t.Error("pendingNote is empty, want a warning for the status screen")
	}

	s, err := loadLastState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "tun" {
		t.Errorf("saved mode = %q, want %q — the fallback must not overwrite it", s.Mode, "tun")
	}
}

// MODE=tun in .env is an explicit demand, not a preference: without root it
// still fails instead of silently doing something else.
func TestResolveModeEnvTunWithoutPrivilegesFails(t *testing.T) {
	a := newModeApp(t, "tun", func() error { return errNoRoot })

	if err := a.resolveMode(); !errors.Is(err, errNoRoot) {
		t.Fatalf("resolveMode() = %v, want %v", err, errNoRoot)
	}
}

// An unusable value in the state file must not decide the mode.
func TestResolveModeIgnoresGarbageSavedMode(t *testing.T) {
	a := newModeApp(t, "proxy", nil)
	if err := saveLastState(LastState{Mode: "nonsense"}); err != nil {
		t.Fatal(err)
	}

	if err := a.resolveMode(); err != nil {
		t.Fatalf("resolveMode() = %v, want nil", err)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q, want %q", a.mode, "proxy")
	}
}

// The mode and the server selection are written by unrelated actions; neither
// may erase the other.
func TestRememberSelectionKeepsMode(t *testing.T) {
	a := newModeApp(t, "proxy", nil)
	a.mode = "tun"
	a.rememberMode()

	a.rememberSelection("https://example.com/sub", &subscription.SubEntry{
		Remarks: "srv", Address: "example.com", Port: 443,
	})

	s, err := loadLastState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "tun" {
		t.Errorf("saved mode = %q, want %q", s.Mode, "tun")
	}
	if s.ServerAddress != "example.com" || s.ServerPort != 443 {
		t.Errorf("server = %s:%d, want example.com:443", s.ServerAddress, s.ServerPort)
	}
}

// Switching mode writes it through without dropping the saved server.
func TestSwitchModePersistsAndKeepsServer(t *testing.T) {
	a := newModeApp(t, "proxy", func() error { return nil })
	a.rememberSelection("https://example.com/sub", &subscription.SubEntry{
		Remarks: "srv", Address: "example.com", Port: 443,
	})

	if err := a.switchMode(); err != nil {
		t.Fatalf("switchMode() = %v, want nil", err)
	}

	s, err := loadLastState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "tun" {
		t.Errorf("saved mode = %q, want %q", s.Mode, "tun")
	}
	if s.ServerAddress != "example.com" {
		t.Errorf("server address = %q, want example.com", s.ServerAddress)
	}
}

// A refused switch leaves both the running mode and the saved state untouched.
func TestSwitchModeRefusedDoesNotPersist(t *testing.T) {
	a := newModeApp(t, "proxy", func() error { return errNoRoot })

	if err := a.switchMode(); !errors.Is(err, errNoRoot) {
		t.Fatalf("switchMode() = %v, want %v", err, errNoRoot)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q, want %q", a.mode, "proxy")
	}
	if _, err := loadLastState(); err == nil {
		t.Error("state file was written for a refused switch, want none")
	}
}
