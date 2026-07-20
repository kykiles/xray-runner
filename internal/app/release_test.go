package app

// Teardown must undo what the session actually turned on. The mode can change
// while the session is still up (the m key switches it before teardown runs),
// so keying the teardown off a.mode leaves the other mode's setup behind.

import (
	"testing"

	"xray-runner/internal/system"
)

type teardownSpy struct {
	killSwitchOff int
	proxyRestored int
}

func newTeardownApp(t *testing.T, spy *teardownSpy) *App {
	t.Helper()
	return &App{
		disableKillSwitch: func() error { spy.killSwitchOff++; return nil },
		restoreProxy:      func(system.ProxyState) error { spy.proxyRestored++; return nil },
	}
}

// tun → proxy: the kill switch was enabled in tun mode and must come down even
// though the app is already in proxy mode by the time teardown runs.
func TestReleaseSessionDisablesKillSwitchAfterSwitchToProxy(t *testing.T) {
	var spy teardownSpy
	a := newTeardownApp(t, &spy)
	a.mode = "tun"
	a.killSwitchOn = true

	a.mode = "proxy" // the user pressed m; teardown has not run yet
	a.releaseSession()

	if spy.killSwitchOff != 1 {
		t.Errorf("kill switch disabled %d times, want 1", spy.killSwitchOff)
	}
	if a.killSwitchOn {
		t.Error("killSwitchOn still set after teardown")
	}
	if spy.proxyRestored != 0 {
		t.Errorf("proxy restored %d times, want 0 — it was never enabled", spy.proxyRestored)
	}
}

// proxy → tun: the system proxy was enabled in proxy mode and must be restored
// even though the app is already in tun mode by the time teardown runs.
func TestReleaseSessionRestoresProxyAfterSwitchToTun(t *testing.T) {
	var spy teardownSpy
	a := newTeardownApp(t, &spy)
	a.mode = "proxy"
	a.proxyTouched = true

	a.mode = "tun" // the user pressed m; teardown has not run yet
	a.releaseSession()

	if spy.proxyRestored != 1 {
		t.Errorf("proxy restored %d times, want 1", spy.proxyRestored)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after teardown")
	}
	if spy.killSwitchOff != 0 {
		t.Errorf("kill switch disabled %d times, want 0 — it was never enabled", spy.killSwitchOff)
	}
}

// Nothing was brought up: teardown touches nothing.
func TestReleaseSessionUndoesNothingWhenNothingEnabled(t *testing.T) {
	var spy teardownSpy
	a := newTeardownApp(t, &spy)
	a.mode = "tun"

	a.releaseSession()

	if spy.killSwitchOff != 0 || spy.proxyRestored != 0 {
		t.Errorf("teardown touched the system with nothing enabled: ks=%d proxy=%d",
			spy.killSwitchOff, spy.proxyRestored)
	}
}
