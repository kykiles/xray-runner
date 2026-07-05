package system

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setupRegMock(t *testing.T, mockScript string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("system tests require Windows")
	}

	dir := t.TempDir()
	regPath := filepath.Join(dir, "reg.bat")
	if err := os.WriteFile(regPath, []byte(mockScript), 0644); err != nil {
		t.Fatalf("write reg mock: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
}

func TestReadProxyStateDisabled(t *testing.T) {
	mockScript := `@echo off
echo.
echo HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings
echo     ProxyEnable    REG_DWORD    0x0
`
	setupRegMock(t, mockScript)

	s := ReadProxyState()
	if s.Enabled {
		t.Errorf("Enabled = true, want false")
	}
}

func TestReadProxyStateEnabled(t *testing.T) {
	mockScript := `@echo off
echo.
echo HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings
echo     ProxyEnable    REG_DWORD    0x1
echo     ProxyServer    REG_SZ       127.0.0.1:8888
echo     ProxyOverride  REG_SZ       <-loopback>;*.local
`
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
