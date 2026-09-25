package xray

import (
	"context"
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

// recordingHandler keeps every logged message.
type recordingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// A line past bufio.Scanner's 64 KiB default used to stop the reader and close
// the pipe; the core's next write then died on SIGPIPE (G12). The line is
// logged in pieces, the pipe stays open, and the core exits on its own.
func TestLogPipeSurvivesAVeryLongLine(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	fmt.Fprintln(os.Stdout, strings.Repeat("x", 200<<10))
	time.Sleep(200 * time.Millisecond) // let a reader that gave up close the pipe
	fmt.Fprintln(os.Stdout, "after the long line")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	binary := filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", binary, filepath.Join(dir, "main.go")).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, out)
	}

	h := &recordingHandler{}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })

	r := New(binary, []byte("{}"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Wait(); err != nil {
		t.Fatalf("the core did not exit cleanly: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	long := 0
	for _, m := range h.msgs {
		if strings.Trim(m, "x") == "" {
			long += len(m)
		}
	}
	if long != 200<<10 {
		t.Errorf("logged %d bytes of the long line, want %d", long, 200<<10)
	}
	if len(h.msgs) == 0 || h.msgs[len(h.msgs)-1] != "after the long line" {
		t.Errorf("the line after the long one was not logged: last of %d messages", len(h.msgs))
	}
}
