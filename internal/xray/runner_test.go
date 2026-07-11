package xray

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildMockXray(t *testing.T, exitCode int, sleep time.Duration) string {
	t.Helper()
	dir := t.TempDir()

	src := fmt.Sprintf(`package main
import "os"
import "time"
func main() {
	time.Sleep(time.Duration(%d))
	os.Exit(%d)
}`, sleep.Nanoseconds(), exitCode)

	// Use "go build" to compile a small test binary
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(src), 0644); err != nil {
		t.Fatalf("write mock source: %v", err)
	}

	binaryName := "xray"
	if runtime.GOOS == "windows" {
		binaryName = "xray.exe"
	}
	outPath := filepath.Join(dir, binaryName)

	cmd := exec.Command("go", "build", "-o", outPath, mainPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, out)
	}
	return outPath
}

func TestRunnerStartStop(t *testing.T) {
	mockBinary := buildMockXray(t, 0, 1*time.Second)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if r.PID() == 0 {
		t.Error("PID() = 0, want non-zero")
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestRunnerRunWithRetry(t *testing.T) {
	mockBinary := buildMockXray(t, 1, 100*time.Millisecond)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)

	// Should give up after 3 retries for a constantly crashing binary
	err := r.RunWithRetry(context.Background(), 3)
	if err == nil {
		t.Fatal("expected error from RunWithRetry, got nil")
	}
}

func TestRunnerRunWithRetryCleanExit(t *testing.T) {
	mockBinary := buildMockXray(t, 0, 500*time.Millisecond)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	ctx := context.Background()

	err := r.RunWithRetry(ctx, 3)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestFindBinary(t *testing.T) {
	mockBinary := buildMockXray(t, 0, 100*time.Millisecond)
	origDir := filepath.Dir(mockBinary)
	origExe := osExecutable

	defer func() { osExecutable = origExe }()
	osExecutable = func() (string, error) {
		return filepath.Join(origDir, "test.exe"), nil
	}

	// Should find our mock binary since we're "running" from its directory
	path := FindBinary()
	if path == "" {
		t.Fatal("FindBinary() returned empty")
	}
}
