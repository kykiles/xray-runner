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
	"sync"
	"time"
)

type Runner struct {
	binary string
	config string
	mu     sync.Mutex
	cmd    *exec.Cmd
}

var osExecutable = os.Executable

func New(binary, configPath string) *Runner {
	return &Runner{binary: binary, config: configPath}
}

func FindBinary() string {
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}

	paths := []string{name}

	if exe, err := osExecutable(); err == nil {
		paths = append([]string{filepath.Join(filepath.Dir(exe), name)}, paths...)
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
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

	go logPipe(stdout, slog.LevelDebug)
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
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := r.Start(ctx); err != nil {
			return err
		}
		slog.Info("xray started", "pid", r.PID())

		err := r.Wait()
		if err == nil || ctx.Err() != nil {
			return nil
		}

		delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		slog.Warn("xray crashed", "attempt", attempt+1, "max_retries", maxRetries, "error", err, "retry_in", delay)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
	return fmt.Errorf("xray crashed %d times, giving up", maxRetries)
}
