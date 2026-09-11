package app

// A09: a core that dies on its own must end the session with a reason the user
// can see, not leave "connected" on screen in front of a dead proxy — and not
// quietly close the whole app either.

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
)

// buildSessionXray builds a mock core that passes "run -test" and, when run for
// real, either dies half a second in (MOCK_XRAY_DIE=1) or stays up.
func buildSessionXray(t *testing.T) string {
	t.Helper()
	src := `package main

import (
	"os"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "-test" {
			return
		}
	}
	if os.Getenv("MOCK_XRAY_DIE") == "1" {
		time.Sleep(500 * time.Millisecond)
		os.Exit(1)
	}
	time.Sleep(30 * time.Second)
}
`
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(src), 0644); err != nil {
		t.Fatalf("write mock source: %v", err)
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, mainPath).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, b)
	}
	return out
}

// newCoreSessionApp is a headless tun session over the mock core, with the
// interface, routing and health loop faked so nothing touches the machine.
// healthUp fires once bring-up is done and the session is running.
func newCoreSessionApp(t *testing.T, unrouted *int, healthUp chan<- struct{}) *App {
	t.Helper()
	a := newTemplateApp(t)
	a.mode = "tun"
	a.binary = buildSessionXray(t)
	a.tmpFile = filepath.Join(t.TempDir(), "xray_config.json")
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	a.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Name: "xray-tun", Flags: net.FlagUp}}, nil
	}
	a.enableTunRouting = func(system.TunRouteConfig) error { return nil }
	a.disableTunRouting = func() error { *unrouted++; return nil }
	a.healthLoop = func(ctx context.Context, _ sessionPorts) {
		close(healthUp)
		<-ctx.Done()
	}
	return a
}

func coreSessionTarget() *target {
	return &target{entry: &subscription.SubEntry{
		Protocol: "vless", Address: "203.0.113.5", Port: 443,
		UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}}
}

func TestRunSession_CoreDeathEndsSessionWithReason(t *testing.T) {
	t.Setenv("MOCK_XRAY_DIE", "1")
	var unrouted int
	a := newCoreSessionApp(t, &unrouted, make(chan struct{}))

	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(context.Background(), coreSessionTarget())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ядро завершилось") {
			t.Fatalf("runSession err = %v, want the core's death as the reason", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("session still up after the core died")
	}
	if unrouted != 1 {
		t.Errorf("tun routes removed %d times, want 1", unrouted)
	}
}

// Ctrl+C stops the core too; that is the user leaving, not the core dying.
func TestRunSession_CancelIsNotCoreDeath(t *testing.T) {
	var unrouted int
	healthUp := make(chan struct{})
	a := newCoreSessionApp(t, &unrouted, healthUp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(ctx, coreSessionTarget())
		done <- err
	}()

	select {
	case <-healthUp:
	case err := <-done:
		t.Fatalf("session ended before it came up: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("session never came up")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runSession err = %v, want context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("session did not end after cancel")
	}
	if unrouted != 1 {
		t.Errorf("tun routes removed %d times, want 1", unrouted)
	}
}
