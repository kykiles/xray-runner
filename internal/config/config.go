package config

import (
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
}

func Load(filenames ...string) (*Config, error) {
	if err := godotenv.Load(filenames...); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	cfg := &Config{
		VlessURL:        os.Getenv("VLESS_URL"),
		SubscriptionURL: os.Getenv("SUBSCRIPTION_URL"),
		Mode:            strings.ToLower(envOr("MODE", "proxy")),
		LogEnabled:      parseBool("LOG_ENABLED", false),
		LogFile:         envOr("LOG_FILE", "xray-runner.log"),
		LogLevel:        strings.ToLower(envOr("LOG_LEVEL", "info")),
		MaskCreds:       parseBool("MASK_CREDENTIALS", true),
		XrayLogLvl:      envOr("XRAY_LOG_LEVEL", "warning"),
		KillSwitch:      parseBool("KILL_SWITCH", false),
	}

	if cfg.Mode != "proxy" && cfg.Mode != "tun" {
		return nil, fmt.Errorf("MODE must be 'proxy' or 'tun', got %q", cfg.Mode)
	}

	if cfg.VlessURL == "" && cfg.SubscriptionURL == "" {
		return nil, fmt.Errorf("either VLESS_URL or SUBSCRIPTION_URL must be set in .env")
	}

	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
