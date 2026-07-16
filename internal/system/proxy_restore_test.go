//go:build windows

package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRegMockLogged like setupRegMock, but reg.bat also logs all `reg add`
// invocations to a file whose path is returned. `reg query` responses are
// controlled by queryBlock (echo lines). Env REG_LOG carries the log path.
func setupRegMockLogged(t *testing.T, queryBlock string) string {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "reg.log")
	t.Setenv("REG_LOG", logPath)

	regBat := `@echo off
if "%1"=="query" (
` + queryBlock + `
)
if "%1"=="add" (
  echo %* >> "%REG_LOG%"
)
`
	regPath := filepath.Join(dir, "reg.bat")
	if err := os.WriteFile(regPath, []byte(regBat), 0644); err != nil {
		t.Fatalf("write reg mock: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	return logPath
}

const proxyEnableQueryBlock = `echo.
echo HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings
echo     ProxyEnable    REG_DWORD    0x0`

// TestRestoreDisabledProxy verifies that Restore writes ProxyEnable=0 when the
// original state was disabled. Regression for the bug where cleanup skipped
// restoration when oldProxy.Enabled == false, leaving 127.0.0.1:10809 active.
func TestRestoreDisabledProxy(t *testing.T) {
	logPath := setupRegMockLogged(t, proxyEnableQueryBlock)

	pm := New()
	if err := pm.Restore(ProxyState{Enabled: false}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal("Restore did not invoke reg add")
	}
	logStr := string(log)
	if !strings.Contains(logStr, "ProxyEnable") {
		t.Errorf("expected reg add ProxyEnable, got: %s", logStr)
	}
	if !strings.Contains(logStr, " REG_DWORD ") || !strings.Contains(logStr, " 0 ") {
		if !strings.Contains(logStr, "/d 0") {
			t.Errorf("expected /d 0 (disabled), got: %s", logStr)
		}
	}
}

// TestRestoreEnabledProxy verifies that Restore writes ProxyEnable=1, the
// saved server, and the saved overrides when the state was enabled.
// Ensures we didn't regress the enabled-state path while fixing the disabled one.
func TestRestoreEnabledProxy(t *testing.T) {
	logPath := setupRegMockLogged(t, proxyEnableQueryBlock)

	pm := New()
	if err := pm.Restore(ProxyState{
		Enabled:   true,
		Server:    "1.2.3.4:80",
		Overrides: "localhost",
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal("Restore did not invoke reg add")
	}
	logStr := string(log)
	// Expect 3 reg add invocations: ProxyEnable, ProxyServer, ProxyOverride.
	calls := strings.Count(logStr, "\n")
	if calls < 3 {
		t.Errorf("expected 3 reg add calls, got %d: %s", calls, logStr)
	}
	if !strings.Contains(logStr, "1.2.3.4:80") {
		t.Errorf("expected ProxyServer=1.2.3.4:80, got: %s", logStr)
	}
	if !strings.Contains(logStr, "localhost") {
		t.Errorf("expected ProxyOverride=localhost, got: %s", logStr)
	}
}
