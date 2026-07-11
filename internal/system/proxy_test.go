//go:build windows

package system

import (
	"os"
	"path/filepath"
	"testing"
)

func setupRegMock(t *testing.T, mockScript string) {
	t.Helper()

	dir := t.TempDir()
	regPath := filepath.Join(dir, "reg.bat")
	if err := os.WriteFile(regPath, []byte(mockScript), 0644); err != nil {
		t.Fatalf("write reg mock: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
}

func TestReadProxyStateDisabled(t *testing.T) {
	mockScript := "@echo off\r\necho.\r\necho HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\necho     ProxyEnable    REG_DWORD    0x0\r\n"
	setupRegMock(t, mockScript)

	s := ReadProxyState()
	if s.Enabled {
		t.Errorf("Enabled = true, want false")
	}
}

func TestReadProxyStateEnabled(t *testing.T) {
	mockScript := "@echo off\r\necho.\r\necho HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings\r\nif /i \"%4\"==\"ProxyEnable\" echo     ProxyEnable    REG_DWORD    0x1\r\nif /i \"%4\"==\"ProxyServer\" echo     ProxyServer    REG_SZ       127.0.0.1:8888\r\nif /i \"%4\"==\"ProxyOverride\" echo     ProxyOverride  REG_SZ       ^<-loopback^>;*.local\r\n"
	setupRegMock(t, mockScript)

	s := ReadProxyState()
	if !s.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if s.Server != "127.0.0.1:8888" {
		t.Errorf("Server = %q, want %q", s.Server, "127.0.0.1:8888")
	}
	if s.Overrides != "<-loopback>;*.local" {
		t.Errorf("Overrides = %q, want %q", s.Overrides, "<-loopback>;*.local")
	}
}
