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
	if cfg.LogEnabled {
		t.Errorf("LogEnabled = true, want false")
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

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogEnabled {
		t.Errorf("LogEnabled = true, want false (fallback)")
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
