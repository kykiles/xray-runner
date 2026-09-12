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

func (pm *ProxyManager) Enable(port int) error {
	saved := ReadProxyState()
	if !saved.Read {
		slog.Warn("исходные настройки прокси не прочитаны — точный откат после сессии невозможен", "key", regKey)
	}

	overrides := formatOverrides(saved.Overrides)

	slog.Debug("enabling system proxy", "port", port, "overrides", overrides)

	rollback := func() {
		if err := WriteProxyState(saved); err != nil {
			slog.Error("не удалось откатить настройки прокси", "error", err)
		}
	}

	if err := regSetInt("ProxyEnable", 1); err != nil {
		return fmt.Errorf("enable proxy: %w", err)
	}
	if err := regSetString("ProxyServer", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		rollback()
		return fmt.Errorf("set proxy server: %w", err)
	}
	if err := regSetString("ProxyOverride", overrides); err != nil {
		rollback()
		return fmt.Errorf("set proxy overrides: %w", err)
	}

	notifyWinINet()
	return nil
}
