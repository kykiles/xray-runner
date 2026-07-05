package xray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type Runner struct {
	binary string
	config string
	cmd    *exec.Cmd
}

func New(binary, configPath string) *Runner {
	return &Runner{binary: binary, config: configPath}
}

func FindBinary() string {
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}

	paths := []string{name}

	if exe, err := os.Executable(); err == nil {
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
	r.cmd = exec.CommandContext(ctx, r.binary, "run", "-c", r.config)
	r.cmd.Stdout = os.Stdout
	r.cmd.Stderr = os.Stderr

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}
	return nil
}

func (r *Runner) Stop() error {
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	if err := r.cmd.Process.Signal(os.Interrupt); err != nil {
		return r.cmd.Process.Kill()
	}
	return nil
}

func (r *Runner) Wait() error {
	return r.cmd.Wait()
}

func (r *Runner) PID() int {
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}
