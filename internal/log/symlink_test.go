package log

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"xray-runner/internal/config"
)

// TUN mode runs under sudo from whatever directory the user launched it in, so
// the log path sits in a place a non-root process can write. A symlink planted
// there must not turn the startup truncate into "wipe any root-owned file" —
// the log is opened, not followed.
func TestInit_RefusesToFollowASymlinkedLogPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink planting is a unix root-escalation scenario")
	}

	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	const content = "must survive"
	if err := os.WriteFile(victim, []byte(content), 0600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	logPath := filepath.Join(dir, "xray-runner.log")
	if err := os.Symlink(victim, logPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cleanup := Init(&config.Config{LogEnabled: true, LogFile: logPath, LogLevel: "info"})
	defer cleanup()

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != content {
		t.Errorf("symlink target was written through: got %q, want %q", got, content)
	}
}
