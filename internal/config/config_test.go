package config

import (
	"os"
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

func TestLoadBadBoolFallback(t *testing.T) {
	os.Clearenv()
	_ = os.Setenv("VLESS_URL", "vless://u@h:1")
	_ = os.Setenv("LOG_ENABLED", "notabool")
	_ = os.Setenv("KILL_SWITCH", "notabool")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.LogEnabled {
		t.Errorf("LogEnabled = false, want true (fallback)")
	}
	if cfg.KillSwitch {
		t.Errorf("KillSwitch = true, want false (fallback)")
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
