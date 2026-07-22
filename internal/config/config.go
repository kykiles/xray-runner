package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
	// HealthCheckURLs are tried in order until one answers. Overridable because
	// an unreachable default (google is blocked in places this tool is used for)
	// reads as "connection lost" and restarts a perfectly healthy core.
	HealthCheckURLs  []string
	BenchConcurrency int
	BenchTimeout     time.Duration
}

// defaultHealthCheckURLs are captive-portal probes: tiny, unauthenticated, and
// answering 204 from several independent operators.
var defaultHealthCheckURLs = []string{
	"https://www.google.com/generate_204",
	"https://connectivitycheck.gstatic.com/generate_204",
	"https://www.cloudflare.com/cdn-cgi/trace",
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
	intOr := func(key string, def int) int {
		v := os.Getenv(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("%s: ожидается положительное число, получено %q", key, v))
			return def
		}
		return n
	}
	durationOr := func(key string, def time.Duration) time.Duration {
		v := os.Getenv(key)
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: ожидается длительность вида 8s, получено %q", key, v))
			return def
		}
		return d
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
		HealthCheckURLs: healthCheckURLs(),
		// Three at a time keeps the measurement honest: each one runs its own xray
		// instance, and a machine juggling more of them measures the CPU, not the
		// servers.
		BenchConcurrency: intOr("BENCH_CONCURRENCY", 3),
		BenchTimeout:     durationOr("BENCH_TIMEOUT", 8*time.Second),
	}

	if cfg.Mode != "proxy" && cfg.Mode != "tun" {
		return nil, fmt.Errorf("MODE must be 'proxy' or 'tun', got %q", cfg.Mode)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return cfg, nil
}

// CheckURLs is the probe list, never empty: a Config assembled by hand (tests,
// or a future caller that skips Load) would otherwise index into nothing.
func (c *Config) CheckURLs() []string {
	if len(c.HealthCheckURLs) == 0 {
		return defaultHealthCheckURLs
	}
	return c.HealthCheckURLs
}

// healthCheckURLs reads the comma-separated override, falling back to the
// built-in list when it is unset or holds nothing usable.
func healthCheckURLs() []string {
	var out []string
	for _, u := range strings.Split(os.Getenv("HEALTH_CHECK_URL"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return defaultHealthCheckURLs
	}
	return out
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
