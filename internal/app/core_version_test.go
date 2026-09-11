package app

import "testing"

// oldCore is the version line of 26.3.27 — what releases/latest still names,
// and a core whose TUN loops the tunnel into itself.
const oldCore = "Xray 26.3.27 (Xray, Penetrates Everything.) abc (go1.26 windows/amd64)"

const oldCoreRefusal = "нужен xray-core 26.7.28 или новее, установлен 26.3.27"

func oldCoreApp(t *testing.T, mode string) *App {
	t.Helper()
	isolateState(t)
	return &App{
		mode:            mode,
		coreVer:         oldCore,
		checkPrivileges: func() error { return nil },
	}
}

// An old core cannot carry TUN, so a run asked to start in it refuses with the
// version it found. Proxy never touches the tunnel and runs on any core.
func TestResolveMode_OldCore(t *testing.T) {
	a := oldCoreApp(t, "tun")
	err := a.resolveMode()
	if err == nil || err.Error() != oldCoreRefusal {
		t.Errorf("tun on 26.3.27: err = %v, want %q", err, oldCoreRefusal)
	}

	a = oldCoreApp(t, "proxy")
	if err := a.resolveMode(); err != nil {
		t.Errorf("proxy on 26.3.27: err = %v, want nil", err)
	}
}

// The m key is the other way into TUN, and it is refused the same way — before
// the running proxy session is torn down.
func TestSwitchMode_OldCoreRefusesTUN(t *testing.T) {
	a := oldCoreApp(t, "proxy")
	err := a.switchMode()
	if err == nil || err.Error() != oldCoreRefusal {
		t.Errorf("switch to tun on 26.3.27: err = %v, want %q", err, oldCoreRefusal)
	}
	if a.mode != "proxy" {
		t.Errorf("mode = %q after a refused switch, want proxy", a.mode)
	}

	a = oldCoreApp(t, "tun")
	if err := a.switchMode(); err != nil || a.mode != "proxy" {
		t.Errorf("switch to proxy on 26.3.27: mode = %q, err = %v; want proxy, nil", a.mode, err)
	}
}
