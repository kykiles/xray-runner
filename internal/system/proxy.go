package system

type ProxyManager struct {
	// ourServer is the "127.0.0.1:PORT" Enable put into the system settings,
	// empty until it succeeds. forceDisable removes only this exact value, so
	// it can never wipe a proxy somebody else configured.
	ourServer string //nolint:unused // Windows only: proxy_windows.go
}

func New() *ProxyManager {
	return &ProxyManager{}
}

// Restore puts the machine back exactly as Enable found it. When that fails,
// the settings still point at 127.0.0.1:PORT, which nothing listens on once we
// exit — that takes the internet away from everything going through the system
// proxy. So a failed exact restore falls back to switching off what we turned
// on; it is a cruder operation and says so in the log.
func (pm *ProxyManager) Restore(s ProxyState) error {
	if err := WriteProxyState(s); err != nil {
		pm.forceDisable()
		return err
	}
	return nil
}

// ClearProxy switches the system proxy off and takes the values Enable writes
// out. It is not a restore: it undoes a setting that is ours — what a run that
// died before its teardown left pointing at a port nothing listens on. The
// switch is written as off rather than deleted, which is how a machine with no
// proxy looks; the server and the overrides go, because on a clean machine they
// do not exist.
func ClearProxy() error {
	return WriteProxyState(ProxyState{Read: true, EnabledSet: true})
}
