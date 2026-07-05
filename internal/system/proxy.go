package system

import (
	"fmt"
	"log/slog"
	"strings"
)

type ProxyManager struct{}

func New() *ProxyManager {
	return &ProxyManager{}
}

func (pm *ProxyManager) Enable(port int) error {
	saved := ReadProxyState()

	overrides := saved.Overrides
	if overrides == "" {
		overrides = "<-loopback>"
	} else if !strings.Contains(overrides, "<-loopback>") {
		overrides += ";-loopback>"
	}

	slog.Debug("enabling system proxy", "port", port, "overrides", overrides)

	if err := execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "1", "/f"); err != nil {
		return fmt.Errorf("enable proxy: %w", err)
	}
	if err := execReg("add", regKey, "/v", "ProxyServer", "/t", "REG_SZ", "/d", fmt.Sprintf("127.0.0.1:%d", port), "/f"); err != nil {
		WriteProxyState(saved)
		return fmt.Errorf("set proxy server: %w", err)
	}
	if err := execReg("add", regKey, "/v", "ProxyOverride", "/t", "REG_SZ", "/d", overrides, "/f"); err != nil {
		WriteProxyState(saved)
		return fmt.Errorf("set proxy overrides: %w", err)
	}
	return nil
}

func (pm *ProxyManager) Disable() error {
	return execReg("add", regKey, "/v", "ProxyEnable", "/t", "REG_DWORD", "/d", "0", "/f")
}

func (pm *ProxyManager) Restore(s ProxyState) error {
	return WriteProxyState(s)
}
