package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("VLESS_URL", "vless://uuid@host:443")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VlessURL != "vless://uuid@host:443" {
		t.Errorf("VlessURL = %q, want %q", cfg.VlessURL, "vless://uuid@host:443")
	}
	if !cfg.LogEnabled {
		t.Errorf("LogEnabled = false, want true")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if !cfg.MaskCreds {
		t.Errorf("MaskCreds = false, want true")
	}
	if cfg.XrayLogLvl != "warning" {
		t.Errorf("XrayLogLvl = %q, want %q", cfg.XrayLogLvl, "warning")
	}
}

func TestLoadMissingURLs(t *testing.T) {
	os.Clearenv()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.VlessURL != "" {
		t.Errorf("VlessURL = %q, want empty", cfg.VlessURL)
	}
	if cfg.SubscriptionURL != "" {
		t.Errorf("SubscriptionURL = %q, want empty", cfg.SubscriptionURL)
	}
}

func TestLoadSubscriptionURL(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("SUBSCRIPTION_URL", "https://example.com/sub")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SubscriptionURL != "https://example.com/sub" {
		t.Errorf("SubscriptionURL = %q, want %q", cfg.SubscriptionURL, "https://example.com/sub")
	}
}

func TestLoadCustomValues(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("VLESS_URL", "vless://u@h:1")
	_ = os.Setenv("LOG_ENABLED", "true")
	_ = os.Setenv("LOG_LEVEL", "debug")
	_ = os.Setenv("LOG_FILE", "custom.log")
	_ = os.Setenv("MASK_CREDENTIALS", "false")
	_ = os.Setenv("XRAY_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.LogEnabled {
		t.Errorf("LogEnabled = false, want true")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.LogFile != "custom.log" {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, "custom.log")
	}
	if cfg.MaskCreds {
		t.Errorf("MaskCreds = true, want false")
	}
	if cfg.XrayLogLvl != "debug" {
		t.Errorf("XrayLogLvl = %q, want %q", cfg.XrayLogLvl, "debug")
	}
}

// Replaces TestLoadBadBoolFallback, which pinned the opposite contract: an
// unreadable value used to fall back to the default silently. That is wrong for
// KILL_SWITCH above all — the user reads "on" and gets "off" — so a bad value is
// now a startup error, exactly as MODE has always been. Every bad value is
// reported at once rather than one per run.
func TestLoadBadBoolIsFatal(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("VLESS_URL", "vless://u@h:1")
	_ = os.Setenv("LOG_ENABLED", "notabool")
	_ = os.Setenv("KILL_SWITCH", "notabool")

	_, err := Load()
	if err == nil {
		t.Fatal("bad bool values accepted silently, want an error")
	}
	for _, key := range []string{"LOG_ENABLED", "KILL_SWITCH"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestLoadHWID(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("SUBSCRIPTION_URL", "https://example.com/sub")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HWID != "" {
		t.Errorf("HWID = %q, want empty", cfg.HWID)
	}
	if cfg.HWIDDeviceModel != "xray-runner" {
		t.Errorf("HWIDDeviceModel = %q, want %q", cfg.HWIDDeviceModel, "xray-runner")
	}
}

func TestLoadHWIDCustom(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("SUBSCRIPTION_URL", "https://example.com/sub")
	_ = os.Setenv("HWID", "my-custom-hwid-value")
	_ = os.Setenv("HWID_DEVICE_MODEL", "custom-app")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HWID != "my-custom-hwid-value" {
		t.Errorf("HWID = %q, want %q", cfg.HWID, "my-custom-hwid-value")
	}
	if cfg.HWIDDeviceModel != "custom-app" {
		t.Errorf("HWIDDeviceModel = %q, want %q", cfg.HWIDDeviceModel, "custom-app")
	}
}

func TestGetOrCreateHWID_Override(t *testing.T) {
	hwid := GetOrCreateHWID("my-override")
	if hwid != "my-override" {
		t.Errorf("got %q, want %q", hwid, "my-override")
	}
}

func TestGetOrCreateHWID_GenerateAndReuse(t *testing.T) {
	os.Remove(hwidFile)
	defer os.Remove(hwidFile)

	hwid1 := GetOrCreateHWID("")
	if len(hwid1) != 20 {
		t.Errorf("expected 20-char hex, got %q (len=%d)", hwid1, len(hwid1))
	}

	data, err := os.ReadFile(hwidFile)
	if err != nil {
		t.Fatalf("hwid.txt not created: %v", err)
	}
	if string(data) != hwid1 {
		t.Errorf("file content = %q, want %q", string(data), hwid1)
	}

	hwid2 := GetOrCreateHWID("")
	if hwid2 != hwid1 {
		t.Errorf("second call = %q, want %q", hwid2, hwid1)
	}
}

func TestGetOrCreateHWID_Persistence(t *testing.T) {
	os.Remove(hwidFile)
	os.WriteFile(hwidFile, []byte("persistent-hwid-value"), 0644)
	defer os.Remove(hwidFile)

	hwid := GetOrCreateHWID("")
	if hwid != "persistent-hwid-value" {
		t.Errorf("got %q, want %q", hwid, "persistent-hwid-value")
	}
}

func TestLoadLogLevelToLower(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("VLESS_URL", "vless://u@h:1")
	_ = os.Setenv("LOG_LEVEL", "WARN")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "warn")
	}
}

// M-1: strconv.ParseBool rejects "yes"/"on", and the silent fallback to the
// default turned KILL_SWITCH=yes into a disabled kill switch — the user believes
// it is on and has no way to find out otherwise.
func TestLoad_RejectsMalformedBool(t *testing.T) {
	for _, v := range []string{"yes", "on", "да", "1.0"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("KILL_SWITCH", v)
			if _, err := Load(); err == nil {
				t.Fatalf("KILL_SWITCH=%q accepted silently, want an error", v)
			}
		})
	}
}

// withGOOS makes Load see another platform for the duration of the test.
func withGOOS(t *testing.T, os string) {
	t.Helper()
	orig := goos
	goos = os
	t.Cleanup(func() { goos = orig })
}

// The values ParseBool does understand keep working.
func TestLoad_AcceptsValidBool(t *testing.T) {
	withGOOS(t, "linux") // a kill switch is refused outright on Windows
	for v, want := range map[string]bool{"true": true, "1": true, "false": false, "0": false, "TRUE": true} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("KILL_SWITCH", v)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("KILL_SWITCH=%q: %v", v, err)
			}
			if cfg.KillSwitch != want {
				t.Errorf("KILL_SWITCH=%q → %v, want %v", v, cfg.KillSwitch, want)
			}
		})
	}
}

// A02: the netsh kill switch blocked xray itself, so Windows refuses the setting
// at start instead of promising a protection it does not deliver.
func TestLoad_KillSwitchRefusedOnWindows(t *testing.T) {
	withGOOS(t, "windows")

	t.Setenv("KILL_SWITCH", "true")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "не поддерживается") {
		t.Fatalf("KILL_SWITCH=true on Windows: err = %v, want a refusal", err)
	}

	t.Setenv("KILL_SWITCH", "false")
	if _, err := Load(); err != nil {
		t.Fatalf("KILL_SWITCH=false on Windows: %v", err)
	}
}

// M-2: the HWID goes straight into an x-hwid request header, where a stray
// newline makes net/http reject the whole request ("invalid header field value")
// — every subscription stops loading with an error that names neither hwid.txt
// nor the newline. The legacy ./hwid.txt path is where such a newline shows up.
func TestGetOrCreateHWID_TrimsWhitespace(t *testing.T) {
	for _, stored := range []string{"abc123def456\n", "  abc123def456  ", "abc123def456\r\n"} {
		t.Run(strconv.Quote(stored), func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("hwid.txt", []byte(stored), 0600); err != nil {
				t.Fatal(err)
			}
			if got := GetOrCreateHWID(""); got != "abc123def456" {
				t.Errorf("HWID = %q, want %q", got, "abc123def456")
			}
		})
	}
}

// The same trimming applies to the config-dir copy, not just the legacy file.
func TestGetOrCreateHWID_TrimsConfigDirCopy(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := hwidPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("deadbeef01\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := GetOrCreateHWID(""); got != "deadbeef01" {
		t.Errorf("HWID = %q, want it trimmed", got)
	}
}
