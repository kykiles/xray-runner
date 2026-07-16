//go:build linux

package system

import (
	"fmt"
	"log/slog"
)

func (pm *ProxyManager) Enable(port int) error {
	saved := ReadProxyState()

	overrides := saved.Overrides
	if overrides == "" {
		overrides = "localhost,127.0.0.0/8,::1"
	}

	slog.Debug("enabling system proxy", "port", port, "overrides", overrides)

	if setDesktopProxy("127.0.0.1", port, overrides) {
		return nil
	}

	_ = WriteProxyState(saved)
	return fmt.Errorf("no supported desktop environment found (tried GNOME gsettings and KDE kwriteconfig5)")
}

func setDesktopProxy(host string, port int, overrides string) bool {
	if writeGsettings(ProxyState{
		Enabled:   true,
		Server:    fmt.Sprintf("%s:%d", host, port),
		Overrides: overrides,
	}) {
		return true
	}
	if writeKDE(ProxyState{
		Enabled:   true,
		Server:    fmt.Sprintf("%s:%d", host, port),
		Overrides: overrides,
	}) {
		return true
	}
	return false
}
