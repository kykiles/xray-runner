package xray

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The core must not outlive a parent killed outright: nothing of ours runs on
// kill -9, and a surviving core keeps its ports and its TUN interface (G07).
// The parent is this test binary, re-run in a helper mode.
func TestCoreDiesWithItsParent(t *testing.T) {
	if os.Getenv("XRAY_TIE_PARENT") == "1" {
		tieParent()
		return
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("the tool ships for Linux and Windows only")
	}
	core := buildMockXray(t, 0, time.Minute)
	pidFile := filepath.Join(t.TempDir(), "pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestCoreDiesWithItsParent$")
	parent.Env = append(os.Environ(), "XRAY_TIE_PARENT=1", "XRAY_TIE_CORE="+core, "XRAY_TIE_PIDFILE="+pidFile)
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}

	var pid int
	waitFor(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil || !strings.HasSuffix(string(data), "\n") {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	})
	if coreGone(pid) {
		t.Fatalf("core %d not running before the parent is killed", pid)
	}

	_ = parent.Process.Kill()
	_ = parent.Wait()

	waitFor(t, func() bool { return coreGone(pid) })
}

// tieParent starts a core the way a session does and then waits to be killed.
func tieParent() {
	r := New(os.Getenv("XRAY_TIE_CORE"), "unused.json")
	if err := r.Start(context.Background()); err != nil {
		os.Exit(3)
	}
	pid := strconv.Itoa(r.PID()) + "\n"
	if err := os.WriteFile(os.Getenv("XRAY_TIE_PIDFILE"), []byte(pid), 0o600); err != nil {
		os.Exit(4)
	}
	time.Sleep(time.Minute)
	os.Exit(5)
}
