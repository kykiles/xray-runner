package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
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
		logFile = config.Path("xray-runner.log")
	}

	// Truncated, not appended: xray's own output lands here, and a TUN session
	// that hits a routing loop writes tens of megabytes in minutes. Keeping only
	// the current run makes the file readable and bounds it by one run.
	f, err := os.OpenFile(logFile, os.O_TRUNC|os.O_CREATE|os.O_WRONLY|openNoFollow, 0600)
	if err != nil {
		// The only stderr write we allow: without it a broken log path would be
		// invisible, since there is no console logging to fall back on.
		fmt.Fprintf(os.Stderr, "не удалось открыть лог-файл %s: %v\n", logFile, err)
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}
	}

	// TUN mode needs root, so the log would otherwise stay root-owned 0600 and
	// unreadable to the user who ran the tool. By descriptor, so the handback
	// cannot be redirected at another file. Best-effort: a failed chown must not
	// cost us the logging itself, but it is worth a word — silently keeping the
	// log root-only is the bug this is here to prevent (L-8).
	if err := system.RestoreSudoOwnerFile(f); err != nil {
		fmt.Fprintf(os.Stderr, "не удалось вернуть владельца лог-файла %s: %v\n", logFile, err)
	}

	w := &cappedWriter{f: f, limit: maxLogBytes}
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	return func() {
		_ = f.Close()
	}
}

// maxLogBytes bounds a single run. Truncating at startup is not enough on its
// own: a TUN session left up for days, or one stuck in a routing loop, never
// restarts the tool and would grow the file without limit.
const maxLogBytes = 5 << 20 // 5 MiB

const truncationNotice = "log truncated: hit the size cap, older lines dropped"

// cappedWriter writes to the log file and starts over from byte 0 once the cap
// is reached, so what survives is the most recent output — the part that
// explains whatever is happening now. xray's stdout and stderr are drained by
// separate goroutines, hence the mutex.
type cappedWriter struct {
	mu    sync.Mutex
	f     *os.File
	n     int64
	limit int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.n+int64(len(p)) > w.limit {
		if err := w.f.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := w.f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		w.n = 0
		// Written straight to the file rather than through slog: this call is
		// already inside a slog handler and holds the lock.
		notice, err := w.f.Write([]byte(truncationNotice + "\n"))
		if err != nil {
			return 0, err
		}
		w.n += int64(notice)
	}

	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
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
