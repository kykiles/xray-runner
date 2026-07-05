package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	VlessURL   string
	LogEnabled bool
	LogFile    string
	LogLevel   string
	MaskCreds  bool
	XrayLogLvl string
}

func Load(filenames ...string) (*Config, error) {
	if err := godotenv.Load(filenames...); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	cfg := &Config{
		VlessURL:   os.Getenv("VLESS_URL"),
		LogEnabled: parseBool("LOG_ENABLED", false),
		LogFile:    envOr("LOG_FILE", "xray-runner.log"),
		LogLevel:   strings.ToLower(envOr("LOG_LEVEL", "info")),
		MaskCreds:  parseBool("MASK_CREDENTIALS", true),
		XrayLogLvl: envOr("XRAY_LOG_LEVEL", "warning"),
	}

	if cfg.VlessURL == "" {
		return nil, fmt.Errorf("VLESS_URL is required but not set in .env")
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
