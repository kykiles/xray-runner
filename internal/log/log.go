package log

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"xray-runner/internal/config"
)

func Init(cfg *config.Config) func() {
	if !cfg.LogEnabled {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		return func() {}
	}

	logFile := cfg.LogFile
	if logFile == "" {
		logFile = "xray-runner.log"
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("cannot open log file, logging to stderr only", "file", logFile, "error", err)
		return func() {}
	}

	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f), &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	return func() {
		f.Close()
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
