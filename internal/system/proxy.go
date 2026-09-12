package system

type ProxyManager struct {
	// ourServer is the "127.0.0.1:PORT" Enable put into the system settings,
	// empty until it succeeds. forceDisable removes only this exact value, so
	// it can never wipe a proxy somebody else configured.
	ourServer string
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
