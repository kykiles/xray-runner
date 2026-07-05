package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"xray-runner/internal/config"
)

type Level slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type FilteredHandler struct {
	handler slog.Handler
	level   slog.Level
	writer  io.Writer
}

func (h *FilteredHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *FilteredHandler) Handle(ctx context.Context, r slog.Record) error {
	if _, err := h.writer.Write([]byte(r.Message + "\n")); err != nil {
		return err
	}
	return h.handler.Handle(ctx, r)
}

func (h *FilteredHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &FilteredHandler{handler: h.handler.WithAttrs(attrs), level: h.level, writer: h.writer}
}

func (h *FilteredHandler) WithGroup(name string) slog.Handler {
	return &FilteredHandler{handler: h.handler.WithGroup(name), level: h.level, writer: h.writer}
}

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

	level := parseLevel(cfg.LogLevel)
	textHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})
	handler := &FilteredHandler{
		handler: textHandler,
		level:   level,
		writer:  f,
	}

	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f), &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	return func() {
		f.Close()
	}
}
