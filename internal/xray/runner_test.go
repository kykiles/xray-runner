package xray

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// Stop only signals the process; whoever starts one without going through
// RunWithRetry has to Wait for it too, or it lingers as a zombie until the app
// exits — the benchmark used to leave one behind per measured server.
func TestRunnerStopThenWaitReapsProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process state is read from /proc")
	}
	mockBinary := buildMockXray(t, 0, 30*time.Second)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := r.PID()

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_ = r.Wait()

	if state, ok := procState(pid); ok && state == "Z" {
		t.Errorf("pid %d is a zombie after Stop+Wait", pid)
	}
}

// procState reads the process state field out of /proc/<pid>/stat, e.g. "Z" for
// a process that died but was never waited for. ok is false once the entry is
// gone, which is the reaped case.
func procState(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	// The comm field is parenthesised and may hold spaces; the state follows it.
	rest := string(data[strings.LastIndexByte(string(data), ')')+1:])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
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

// A09: xray exiting 0 on its own is a dead core like any other. Returning nil
// for it left the status screen saying "connected" in front of nothing.
func TestRunnerRunWithRetryImmediateCleanExitFails(t *testing.T) {
	mockBinary := buildMockXray(t, 0, 500*time.Millisecond)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	if err := r.RunWithRetry(context.Background(), 3); err == nil {
		t.Fatal("a core that exited 0 by itself was taken for a clean shutdown")
	}
}

// A clean exit after the core had been up goes through the same restart budget
// as a crash, and ends in an error once the budget is spent.
func TestRunnerRunWithRetryRestartsCleanExit(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("XRAY_COUNTER", counter)
	mockBinary := buildCountingXray(t, 2100*time.Millisecond)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, []byte("{}"), 0644)

	r := New(mockBinary, configPath)
	err := r.RunWithRetry(context.Background(), 2)
	if err == nil {
		t.Fatal("RunWithRetry returned nil after the core kept exiting")
	}
	if n := startCount(t, counter); n != 2 {
		t.Errorf("core started %d times, want 2 — a clean exit must be restarted", n)
	}
}

// buildCountingXray builds a mock that records each start (appending to the
// file named by env XRAY_COUNTER), stays up for sleep and exits 0.
func buildCountingXray(t *testing.T, sleep time.Duration) string {
	t.Helper()
	dir := t.TempDir()

	src := fmt.Sprintf(`package main
import (
	"os"
	"time"
)
func main() {
	if p := os.Getenv("XRAY_COUNTER"); p != "" {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		f.WriteString("x")
		f.Close()
	}
	time.Sleep(time.Duration(%d))
}`, sleep.Nanoseconds())
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

// buildChattyXray builds a mock that floods stdout and stderr, then exits.
func buildChattyXray(t *testing.T, lines int) string {
	t.Helper()
	dir := t.TempDir()

	src := fmt.Sprintf(`package main
import (
	"fmt"
	"os"
)
func main() {
	for i := 0; i < %d; i++ {
		fmt.Fprintf(os.Stdout, "stdout line %%d\n", i)
		fmt.Fprintf(os.Stderr, "stderr line %%d\n", i)
	}
}`, lines)

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

type countingHandler struct {
	mu sync.Mutex
	n  int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error {
	// Real logging writes to a file through slog's formatter, which is far
	// slower than draining a pipe. Without that lag the reader always finishes
	// first and the bug hides.
	time.Sleep(50 * time.Microsecond)
	h.mu.Lock()
	h.n++
	h.mu.Unlock()
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }
func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

// M-3: os/exec documents that calling Wait before all reads from StdoutPipe have
// finished is incorrect — Wait closes the pipes underneath the logging
// goroutines. The lost tail is exactly xray's "Failed to start" output, the one
// thing that explains a session that never came up.
func TestRunnerDrainsOutputBeforeWaitReturns(t *testing.T) {
	const lines = 4000
	binary := buildChattyXray(t, lines)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &countingHandler{}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })

	r := New(binary, configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if got := h.count(); got != lines*2 {
		t.Errorf("logged %d of %d lines by the time Wait returned; the rest was lost when Wait closed the pipes", got, lines*2)
	}
}

// TUN and split need 26.7.28: 26.3.27 — still what releases/latest points at —
// has no TUN "gateway" and loops the tunnel into itself, and 26.6.27 is older
// than the pinned core too. The whole major.minor.patch triple counts.
func TestMeetsMinVersion(t *testing.T) {
	cases := map[string]bool{
		"Xray 26.3.27 (Xray, Penetrates Everything.) abc (go1.26 windows/amd64)":     false,
		"Xray 26.6.27 (Xray, Penetrates Everything.) abc (go1.26 windows/amd64)":     false,
		"Xray 26.7.28 (Xray, Penetrates Everything.) 5ca6f4b (go1.26.5 linux/amd64)": true,
		"Xray 26.9.9 (Xray, Penetrates Everything.) abc (go1.26 linux/amd64)":        true,
		"Xray 27.1.1 (Xray, Penetrates Everything.) abc":                             true,
		"Xray 25.12.8 (Xray, Penetrates Everything.) abc (go1.24 windows/amd64)":     false,
		"Xray 1.8.4 (Xray, Penetrates Everything.) abc":                              false,
		// Unparseable: let the core answer rather than refuse over a version line.
		"something else entirely": true,
	}
	for version, want := range cases {
		if got := MeetsMinVersion(version); got != want {
			t.Errorf("MeetsMinVersion(%q) = %v, want %v", version, got, want)
		}
	}
}
