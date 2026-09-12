//go:build windows

package system

import (
	"fmt"
	"log/slog"
	"strings"
)

func formatOverrides(saved string) string {
	if saved == "" {
		return "<-loopback>"
	}
	if strings.Contains(saved, "<-loopback>") {
		return saved
	}
	return saved + ";<-loopback>"
}

// Enable switches the system proxy over to us. It works from the snapshot the
// caller took — the same one teardown restores from — because a snapshot we
// could not read forbids an exact restore (see WriteProxyState). Writing the
// registry anyway would leave 127.0.0.1:PORT behind after the session and cut
// off everything that goes through WinINet: Explorer, Office, most installers,
// Chrome and Edge in their default configuration.
func (pm *ProxyManager) Enable(port int, saved ProxyState) error {
	if !saved.Read {
		return fmt.Errorf("системный прокси не включён: не прочитаны исходные настройки (%s) — откат после сессии был бы невозможен", regKey)
	}

	overrides := formatOverrides(saved.Overrides)
	server := fmt.Sprintf("127.0.0.1:%d", port)

	slog.Debug("enabling system proxy", "port", port, "overrides", overrides)

	rollback := func() {
		if err := WriteProxyState(saved); err != nil {
			slog.Error("не удалось откатить настройки прокси", "error", err)
			pm.forceDisable()
			return
		}
		pm.ourServer = ""
	}

	if err := regSetInt("ProxyEnable", 1); err != nil {
		return fmt.Errorf("enable proxy: %w", err)
	}
	if err := regSetString("ProxyServer", server); err != nil {
		rollback()
		return fmt.Errorf("set proxy server: %w", err)
	}
	// From here the registry names our port: a failed rollback has to clean it.
	pm.ourServer = server
	if err := regSetString("ProxyOverride", overrides); err != nil {
		rollback()
		return fmt.Errorf("set proxy overrides: %w", err)
	}

	notifyWinINet()
	return nil
}

// forceDisable is the last resort after an exact restore failed: the settings
// still name our port, so switch the proxy off and take our own server string
// out. A value somebody else put there is left alone — we could not read it or
// could not write it back, but it is not ours to erase.
func (pm *ProxyManager) forceDisable() {
	if pm.ourServer == "" {
		return
	}
	if err := regSetInt("ProxyEnable", 0); err != nil {
		slog.Error("аварийное выключение прокси не удалось", "key", regKey, "error", err)
		return
	}
	if v, err := regGetString("ProxyServer"); err == nil && v == pm.ourServer {
		if err := regDelete("ProxyServer"); err != nil {
			slog.Warn("не удалось удалить наш ProxyServer", "key", regKey, "error", err)
		}
	}
	pm.ourServer = ""
	notifyWinINet()
	slog.Warn("точный откат прокси не удался — системный прокси выключен аварийно", "key", regKey)
}
