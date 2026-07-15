package xray

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Runner struct {
	binary  string
	config  string
	mu      sync.Mutex
	cmd     *exec.Cmd
	restart bool // set by RequestRestart, consumed by RunWithRetry
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

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("xray не найден: положите %s рядом с исполняемым файлом или в PATH", name)
}

// Version returns the first line of `xray version`. Failure is non-fatal; the
// caller only logs it (X-4).
func Version(binary string) (string, error) {
	out, err := exec.Command(binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("xray version: %w", err)
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return line, nil
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
	go logPipe(stdout, slog.LevelInfo)
	go logPipe(stderr, slog.LevelWarn)
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
// code, then stops the current process. Without this explicit flag a SIGINT
// stop makes xray exit with code 0, which RunWithRetry would treat as a clean
// shutdown and never restart.
func (r *Runner) RequestRestart() {
	r.mu.Lock()
	r.restart = true
	r.mu.Unlock()
	r.Stop()
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

		// Natural exit with code 0 and no restart requested: nothing to
		// recover. Return so the caller cancels its context and shuts down
		// cleanly instead of hanging with a dead xray behind a live proxy.
		if err == nil {
			return nil
		}

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

		delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		attempt++
		slog.Warn("xray crashed", "attempt", attempt, "max_retries", maxRetries, "error", err, "retry_in", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
	return fmt.Errorf("xray crashed %d times, giving up", maxRetries)
}
