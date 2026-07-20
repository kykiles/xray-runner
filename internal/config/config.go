package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	VlessURL        string
	SubscriptionURL string
	Mode            string
	LogEnabled      bool
	LogFile         string
	LogLevel        string
	MaskCreds       bool
	XrayLogLvl      string
	KillSwitch      bool
	HWID            string
	HWIDDeviceModel string
	AllowInsecure   bool
}

func Load(filenames ...string) (*Config, error) {
	if err := godotenv.Load(filenames...); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	// Bad values are collected rather than returned one at a time, so a broken
	// .env reports every mistake in one go instead of one per run.
	var errs []error
	boolOr := func(key string, def bool) bool {
		b, err := parseBool(key, def)
		if err != nil {
			errs = append(errs, err)
		}
		return b
	}

	cfg := &Config{
		VlessURL:        os.Getenv("VLESS_URL"),
		SubscriptionURL: os.Getenv("SUBSCRIPTION_URL"),
		Mode:            strings.ToLower(envOr("MODE", "proxy")),
		LogEnabled:      boolOr("LOG_ENABLED", true),
		LogFile:         envOr("LOG_FILE", "xray-runner.log"),
		LogLevel:        strings.ToLower(envOr("LOG_LEVEL", "info")),
		MaskCreds:       boolOr("MASK_CREDENTIALS", true),
		XrayLogLvl:      envOr("XRAY_LOG_LEVEL", "warning"),
		KillSwitch:      boolOr("KILL_SWITCH", false),
		HWID:            os.Getenv("HWID"),
		HWIDDeviceModel: envOr("HWID_DEVICE_MODEL", "xray-runner"),
		AllowInsecure:   boolOr("ALLOW_INSECURE", false),
	}

	if cfg.Mode != "proxy" && cfg.Mode != "tun" {
		return nil, fmt.Errorf("MODE must be 'proxy' or 'tun', got %q", cfg.Mode)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseBool refuses a value it cannot read instead of quietly falling back to
// the default: KILL_SWITCH=yes used to leave the kill switch off while the user
// believed it was on, with nothing anywhere to say otherwise.
func parseBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: ожидается true или false, получено %q", key, v)
	}
	return b, nil
}
