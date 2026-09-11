package xray

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/bundle"
)

type Runner struct {
	binary  string
	config  string
	mu      sync.Mutex
	cmd     *exec.Cmd
	restart bool // set by RequestRestart, consumed by RunWithRetry
	// pipes tracks the stdout/stderr drain goroutines of the current process.
	// os/exec closes the pipes inside Wait, so Wait must not run until the
	// goroutines have finished reading (M-3).
	pipes sync.WaitGroup
}

var osExecutable = os.Executable

func New(binary, configPath string) *Runner {
	return &Runner{binary: binary, config: configPath}
}

// FindBinary locates the xray executable next to our own binary or on PATH.
// H-3: the current working directory is deliberately excluded so a planted
// ./xray cannot be executed (a real risk under sudo in a writable directory).
func FindBinary() (string, error) {
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}

	if exe, err := osExecutable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// A single-executable build carries the core inside itself; it is unpacked
	// into the user's cache on first run. A core placed next to the app still
	// wins, so a newer one can be dropped in by hand.
	if dir, err := bundle.Dir(); err != nil {
		slog.Warn("встроенное ядро не распаковано", "error", err)
	} else if dir != "" {
		return filepath.Join(dir, name), nil
	}

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("xray не найден: положите %s рядом с исполняемым файлом или в PATH", name)
}

// Version returns the first line of `xray version`. Failure is non-fatal; the
// caller only logs it (X-4). The timeout is internal so the signature stays put:
// this runs on the startup path, and a wedged binary would otherwise hang the
// app before it ever draws a screen.
func Version(binary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("xray version: %w", err)
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return line, nil
}

// MinVersion is the oldest core TUN and split routing run on, and the core
// scripts/deploy.sh pins into bundles — it reads this line, so keep its shape.
// 26.3.27, which releases/latest still names, has no TUN "gateway" and loops
// the tunnel into itself.
const MinVersion = "26.7.28"

// MeetsMinVersion reports whether a version line names a core at MinVersion or
// newer, comparing the full major.minor.patch triple. An unreadable line is
// taken as new enough: refusing to start over a version string we failed to
// parse is worse than letting the core answer.
func MeetsMinVersion(version string) bool {
	minV, _ := parseTriple(MinVersion)
	for f := range strings.FieldsSeq(version) {
		if v, ok := parseTriple(f); ok {
			return slices.Compare(v, minV) >= 0
		}
	}
	return true
}

// parseTriple reads "26.7.28" into its three numbers.
func parseTriple(s string) ([]int, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nil, false
	}
	v := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		v[i] = n
	}
	return v, true
}

func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cmd = exec.CommandContext(ctx, r.binary, "run", "-c", r.config)

	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := r.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}

	// xray prints its fatal "Failed to start" on stdout, so DEBUG would hide the
	// one line that explains why a session never came up.
	r.pipes.Add(2)
	go func() { defer r.pipes.Done(); logPipe(stdout, slog.LevelInfo) }()
	go func() { defer r.pipes.Done(); logPipe(stderr, slog.LevelWarn) }()
	return nil
}

func logPipe(rc io.ReadCloser, level slog.Level) {
	defer rc.Close()
	scanner := bufio.NewScanner(rc)
	for scanner.Scan() {
		slog.Log(context.Background(), level, scanner.Text())
	}
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
		return r.cmd.Process.Kill()
	}
	return nil
}

// RequestRestart asks RunWithRetry to restart xray regardless of the exit
// code, then stops the current process. Without this explicit flag the SIGINT
// stop would count as a crash: backoff and a spent attempt for a restart that
// was asked for.
func (r *Runner) RequestRestart() {
	r.mu.Lock()
	r.restart = true
	r.mu.Unlock()
	_ = r.Stop()
}

// takeRestart reports whether a restart was requested and clears the flag.
func (r *Runner) takeRestart() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.restart
	r.restart = false
	return v
}

// TestConfig validates the config without starting the tunnel. A failure here
// is a deterministic config error and must not be retried.
func (r *Runner) TestConfig(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, r.binary, "run", "-test", "-c", r.config).CombinedOutput()
	if err != nil {
		return fmt.Errorf("config test failed: %w\n%s", err, lastLines(out, 10))
	}
	return nil
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (r *Runner) Wait() error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd == nil {
		return fmt.Errorf("Wait called before Start")
	}
	// M-3: cmd.Wait closes the pipes, so the drain goroutines have to be done
	// first — otherwise xray's last output, including the line explaining why it
	// failed to start, is dropped mid-read.
	r.pipes.Wait()
	return cmd.Wait()
}

func (r *Runner) PID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}

func (r *Runner) RunWithRetry(ctx context.Context, maxRetries int) error {
	attempt := 0
	var lastErr error
	for attempt < maxRetries {
		start := time.Now()
		if err := r.Start(ctx); err != nil {
			return err
		}
		slog.Info("xray started", "pid", r.PID())

		err := r.Wait()

		if ctx.Err() != nil {
			return nil
		}

		// Explicit restart request (health check): restart regardless of exit
		// code — xray exits 0 on SIGINT, so exit code alone can't signal it.
		if r.takeRestart() {
			slog.Info("restart requested by health check, restarting xray")
			attempt = 0
			continue
		}

		// A09: an exit nobody asked for is a dead core, exit code 0 included.
		// Returning nil for it left the session "connected" in front of nothing.
		if err == nil {
			err = errors.New("exit status 0")
		}
		lastErr = err

		// An almost-immediate exit is a deterministic config/runtime error;
		// retrying it just burns the budget without changing the outcome.
		if time.Since(start) < 2*time.Second {
			return fmt.Errorf("xray exited immediately (%v), likely a config error: %w", time.Since(start), err)
		}

		// R-1: a process that stayed up long enough counts as stable; isolated
		// crashes after long uptime shouldn't exhaust the retry budget.
		if time.Since(start) > 60*time.Second {
			attempt = 0
		}

		delay := time.Duration(1<<attempt) * time.Second
		attempt++
		slog.Warn("xray crashed", "attempt", attempt, "max_retries", maxRetries, "error", err, "retry_in", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
	return fmt.Errorf("xray crashed %d times, giving up: %w", maxRetries, lastErr)
}
