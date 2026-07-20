package log

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xray-runner/internal/config"
)

// Xray's own output goes through this file, and a busy TUN session writes it by
// the megabyte. Keeping older runs around buried the current one, so each run
// starts from an empty file.
func TestInit_KeepsOnlyTheCurrentRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray-runner.log")
	cfg := &config.Config{LogEnabled: true, LogFile: path, LogLevel: "info"}

	closeFirst := Init(cfg)
	slog.Info("previous run")
	closeFirst()

	closeSecond := Init(cfg)
	slog.Info("current run")
	closeSecond()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "previous run") {
		t.Errorf("log still carries the previous run: %s", data)
	}
	if !strings.Contains(string(data), "current run") {
		t.Errorf("log lost the current run: %s", data)
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"  Debug  ", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"xyz", slog.LevelInfo},
		{"trace", slog.LevelInfo},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := parseLogLevel(c.in); got != c.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
