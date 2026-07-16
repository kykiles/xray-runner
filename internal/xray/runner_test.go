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

// buildSignalXray builds a mock that records each start (appending to the file
// named by env XRAY_COUNTER) and exits 0 on SIGINT/SIGTERM — mimicking how
// xray-core exits cleanly on a stop signal.
func buildSignalXray(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	src := `package main
import (
	"os"
	"os/signal"
	"syscall"
	"time"
)
func main() {
	if p := os.Getenv("XRAY_COUNTER"); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		f.WriteString("x")
		f.Close()
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	select {
	case <-ch:
		os.Exit(0)
	case <-time.After(10 * time.Second):
		os.Exit(0)
	}
}`
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(src), 0644); err != nil {
		t.Fatalf("write mock source: %v", err)
	}
	binaryName := "xray"
	if runtime.GOOS == "windows" {
		binaryName = "xray.exe"
	}
	outPath := filepath.Join(dir, binaryName)
	if out, err := exec.Command("go", "build", "-o", outPath, mainPath).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, out)
	}
	return outPath
}

func startCount(t *testing.T, counter string) int {
	t.Helper()
	b, err := os.ReadFile(counter)
	if err != nil {
		return 0
	}
	return len(b)
}

// TestRunnerRequestRestart verifies C-1: a requested restart restarts xray even
// when the process exits with code 0 (as it does on SIGINT).
func TestRunnerRequestRestart(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("XRAY_COUNTER", counter)
	mockBinary := buildSignalXray(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.RunWithRetry(ctx, 5) }()

	waitFor(t, func() bool { return startCount(t, counter) >= 1 })
	r.RequestRestart()
	// A second start proves the restart happened despite the clean exit.
	waitFor(t, func() bool { return startCount(t, counter) >= 2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWithRetry after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunWithRetry did not return after cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestRunnerImmediateExitFatal verifies X-1: an almost-immediate crash is
// fatal and returns without burning through the retry backoff.
func TestRunnerImmediateExitFatal(t *testing.T) {
	mockBinary := buildMockXray(t, 1, 50*time.Millisecond)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	start := time.Now()
	err := r.RunWithRetry(context.Background(), 5)
	if err == nil {
		t.Fatal("expected fatal error for immediate exit")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("RunWithRetry retried an immediate crash (took %v), want fast fatal return", elapsed)
	}
}

func TestRunnerTestConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	okBinary := buildMockXray(t, 0, 0)
	if err := New(okBinary, configPath).TestConfig(context.Background()); err != nil {
		t.Fatalf("TestConfig ok binary: %v", err)
	}

	badBinary := buildMockXray(t, 1, 0)
	if err := New(badBinary, configPath).TestConfig(context.Background()); err == nil {
		t.Fatal("TestConfig expected error for failing binary")
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
	path, err := FindBinary()
	if err != nil {
		t.Fatalf("FindBinary() error: %v", err)
	}
	if path == "" {
		t.Fatal("FindBinary() returned empty")
	}
}

func TestFindBinaryNotFound(t *testing.T) {
	origExe := osExecutable
	defer func() { osExecutable = origExe }()
	// Point at an empty temp dir and clear PATH so nothing is found.
	dir := t.TempDir()
	osExecutable = func() (string, error) { return filepath.Join(dir, "test.exe"), nil }
	t.Setenv("PATH", "")

	if _, err := FindBinary(); err == nil {
		t.Fatal("expected error when xray is not found, got nil")
	}
}
