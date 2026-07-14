//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os/exec"
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

	WriteProxyState(saved)
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

func unsetDesktopProxy() bool {
	if exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "none").Run() == nil {
		return true
	}
	if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", "0").Run() == nil {
		return true
	}
	return false
}
