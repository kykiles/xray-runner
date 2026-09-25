//go:build linux

package system

import (
	"fmt"
	"log/slog"
)

func (pm *ProxyManager) Enable(port int, saved ProxyState) error {
	overrides := saved.Overrides
	if overrides == "" {
		overrides = "localhost,127.0.0.0/8,::1"
	}

	slog.Debug("enabling system proxy", "port", port, "overrides", overrides)

	server := fmt.Sprintf("127.0.0.1:%d", port)
	if writeDesktop(ProxyState{Enabled: true, Server: server, SecureServer: server, Overrides: overrides}) {
		return nil
	}

	_ = WriteProxyState(saved)
	return fmt.Errorf("no supported desktop environment found (tried GNOME gsettings and KDE kwriteconfig)")
}

// forceDisable is the fallback Restore calls when the exact restore failed.
// Only Windows needs one: there the settings live in the registry and keep
// pointing at our dead port. Here they go through gsettings/kwriteconfig,
// which either apply or report failure right away.
func (pm *ProxyManager) forceDisable() {}
