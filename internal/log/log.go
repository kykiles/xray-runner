package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"xray-runner/internal/config"
)

// Init routes slog to the log file only: the TUI owns the terminal, so nothing
// may leak to stderr while a menu is on screen.
func Init(cfg *config.Config) func() {
	if !cfg.LogEnabled {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}
	}

	logFile := cfg.LogFile
	if logFile == "" {
		logFile = "xray-runner.log"
	}

	// Truncated, not appended: xray's own output lands here, and a TUN session
	// that hits a routing loop writes tens of megabytes in minutes. Keeping only
	// the current run makes the file readable and bounds it by one run.
	f, err := os.OpenFile(logFile, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		// The only stderr write we allow: without it a broken log path would be
		// invisible, since there is no console logging to fall back on.
		fmt.Fprintf(os.Stderr, "⚠ не удалось открыть лог-файл %s: %v\n", logFile, err)
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	return func() {
		_ = f.Close()
	}
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
