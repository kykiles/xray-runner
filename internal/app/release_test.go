package app

// Teardown must undo what the session actually turned on. The mode can change
// while the session is still up (the m key switches it before teardown runs),
// so keying the teardown off a.mode leaves the other mode's setup behind.

import (
	"errors"
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

// Teardown restores the proxy first, before it waits for the core, and
// releaseSession runs the same step again afterwards. The second call must find
// nothing to do: the settings are already the user's, and writing them twice
// would restore them over a proxy the next session has meanwhile enabled.
func TestRestoreSystemProxyRunsOnce(t *testing.T) {
	var spy teardownSpy
	a := newTeardownApp(t, &spy)
	a.proxyTouched = true

	a.restoreSystemProxy()
	a.releaseSession()

	if spy.proxyRestored != 1 {
		t.Errorf("proxy restored %d times, want 1", spy.proxyRestored)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after teardown")
	}
}

// A kill switch that would not come out stays the session's: the teardown at
// exit tries again instead of forgetting a DROP chain still in place (G08).
func TestReleaseSession_KeepsKillSwitchItFailedToRemove(t *testing.T) {
	calls := 0
	a := &App{
		killSwitchOn: true,
		disableKillSwitch: func() error {
			calls++
			if calls == 1 {
				return errors.New("iptables: цепочка XRAY_KILL осталась")
			}
			return nil
		},
	}

	a.releaseSession()
	if !a.killSwitchOn {
		t.Fatal("killSwitchOn dropped although the chain is still in")
	}
	a.releaseSession()
	if calls != 2 || a.killSwitchOn {
		t.Errorf("after a second teardown: %d removals, killSwitchOn = %v; want 2, false", calls, a.killSwitchOn)
	}
}

// A restore that failed is not forgotten: releaseSession, right after the
// early call in teardown, tries again, and once it goes through nothing more
// is written.
func TestRestoreSystemProxyRetriesAfterFailure(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			if calls == 1 {
				return errors.New("gsettings: временно недоступен")
			}
			return nil
		},
		readProxyState: func() system.ProxyState {
			return system.ProxyState{Enabled: true, Server: "127.0.0.1:10809"}
		},
		proxyTouched: true,
		proxyAddr:    "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	if !a.proxyTouched {
		t.Fatal("proxyTouched cleared by a restore that failed")
	}
	a.releaseSession()
	a.releaseSession()

	if calls != 2 {
		t.Errorf("restore called %d times, want 2 (the failure and one retry)", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after the retry went through")
	}
}

// A retry finds a proxy somebody set after us: it is theirs, and not written
// over with the snapshot from before the session.
func TestRestoreSystemProxyLeavesSomebodyElsesProxy(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			return errors.New("отказ")
		},
		readProxyState: func() system.ProxyState {
			return system.ProxyState{Enabled: true, Server: "10.0.0.1:3128"}
		},
		proxyTouched: true,
		proxyAddr:    "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	a.restoreSystemProxy()

	if calls != 1 {
		t.Errorf("restore called %d times, want 1: the retry must see the proxy is not ours", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set for a proxy that is not ours any more")
	}
}

// The fallback after a failed restore switched the proxy off: putting the
// user's setting back is still owed.
func TestRestoreSystemProxyRetriesAfterFallbackSwitchedOff(t *testing.T) {
	calls := 0
	a := &App{
		restoreProxy: func(system.ProxyState) error {
			calls++
			if calls == 1 {
				return errors.New("отказ")
			}
			return nil
		},
		readProxyState: func() system.ProxyState { return system.ProxyState{Enabled: false} },
		proxyTouched:   true,
		proxyAddr:      "127.0.0.1:10809",
	}

	a.restoreSystemProxy()
	a.restoreSystemProxy()

	if calls != 2 {
		t.Errorf("restore called %d times, want 2", calls)
	}
	if a.proxyTouched {
		t.Error("proxyTouched still set after the retry went through")
	}
}
