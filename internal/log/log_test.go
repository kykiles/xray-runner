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

// A session that hits a routing loop writes megabytes without ever restarting
// the tool, so the per-run truncation alone cannot bound the file.
func TestCappedWriter_StaysUnderLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capped.log")
	f, err := os.OpenFile(path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	w := &cappedWriter{f: f, limit: 1024}
	line := strings.Repeat("x", 100) + "\n"
	for range 200 { // 20 KB through a 1 KB cap
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if _, err := w.Write([]byte("last line\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() > 1024 {
		t.Errorf("log grew past the cap: %d bytes", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Truncation must drop the old bytes, not the ones being written now.
	if !strings.Contains(string(data), "last line") {
		t.Errorf("log lost the newest line: %s", data)
	}
	if !strings.Contains(string(data), truncationNotice) {
		t.Errorf("log does not say it was truncated: %s", data)
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
