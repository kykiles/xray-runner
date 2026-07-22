package config

import (
	"testing"
	"time"
)

func TestHealthAndBenchKnobs(t *testing.T) {
	t.Setenv("HEALTH_CHECK_URL", "https://a.example/204, https://b.example/204")
	t.Setenv("BENCH_CONCURRENCY", "5")
	t.Setenv("BENCH_TIMEOUT", "12s")

	cfg, err := Load(missingEnvFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.HealthCheckURLs) != 2 || cfg.HealthCheckURLs[1] != "https://b.example/204" {
		t.Errorf("HealthCheckURLs = %v, want the two URLs from the env", cfg.HealthCheckURLs)
	}
	if cfg.BenchConcurrency != 5 || cfg.BenchTimeout != 12*time.Second {
		t.Errorf("bench knobs = %d / %v, want 5 / 12s", cfg.BenchConcurrency, cfg.BenchTimeout)
	}
}

// A knob that cannot be read is reported, not silently replaced by the default:
// a BENCH_TIMEOUT of "8" would otherwise leave the user believing they changed
// something.
func TestBadKnobsAreReported(t *testing.T) {
	for _, c := range []struct{ key, val string }{
		{"BENCH_CONCURRENCY", "many"},
		{"BENCH_CONCURRENCY", "0"},
		{"BENCH_TIMEOUT", "8"},
	} {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			if _, err := Load(missingEnvFile); err == nil {
				t.Errorf("Load() accepted %s=%q", c.key, c.val)
			}
		})
	}
}

func TestCheckURLsNeverEmpty(t *testing.T) {
	if got := (&Config{}).CheckURLs(); len(got) == 0 {
		t.Error("CheckURLs() on a hand-built Config is empty; callers index into it")
	}
	cfg := &Config{HealthCheckURLs: []string{"https://only.example"}}
	if got := cfg.CheckURLs(); len(got) != 1 || got[0] != "https://only.example" {
		t.Errorf("CheckURLs() = %v, want the configured URL", got)
	}
}

// missingEnvFile keeps Load off the repo's own .env, whose values would drown
// out the environment the test sets up.
const missingEnvFile = "testdata/does-not-exist.env"
