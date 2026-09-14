package app

// C01: a restarted core brings up a new TUN interface, and the routes on the old
// one die with it. The session has to set the tunnel up again around every core
// process — and end, with the reason, when it cannot.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xraycfg"
)

// buildGenerationXray builds a mock core that passes "run -test", records every
// start in the file named by MOCK_XRAY_STARTS, crashes 2.5s in on its first
// MOCK_XRAY_CRASHES starts (long enough not to count as an immediate exit) and
// stays up on the rest.
func buildGenerationXray(t *testing.T) string {
	t.Helper()
	src := `package main

import (
	"os"
	"strconv"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "-test" {
			return
		}
	}
	p := os.Getenv("MOCK_XRAY_STARTS")
	b, _ := os.ReadFile(p)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	f.WriteString("x")
	f.Close()
	if n, _ := strconv.Atoi(os.Getenv("MOCK_XRAY_CRASHES")); len(b) < n {
		time.Sleep(2500 * time.Millisecond)
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

// tunNet is the machine as a tun session sees it: the TUN interface exists only
// while the core that made it runs, comes up a moment after the core does, and
// gets a new index with every core. Routes can only be put on a live interface.
// Every change the session makes lands in the event log.
type tunNet struct {
	a *App

	mu          sync.Mutex
	index       map[int]int       // core pid → index of its interface
	born        map[int]time.Time // core pid → when its interface was first asked for
	never       bool              // the interface never comes up
	routes      int               // enableTunRouting calls
	unroutes    int               // disableTunRouting calls
	failRoute   map[int]error     // the n-th enableTunRouting call fails with this
	failUnroute map[int]error     // the n-th disableTunRouting call fails with this
	log         []string
}

func newTunGenerationApp(t *testing.T, crashes int) (*App, *tunNet) {
	t.Helper()
	a := newTemplateApp(t)
	a.mode = "tun"
	a.cfg.KillSwitch = true
	a.cfg.HealthCheckURLs = []string{"http://127.0.0.1:1/"}
	a.binary = buildGenerationXray(t)
	a.tmpFile = filepath.Join(t.TempDir(), "xray_config.json")
	t.Setenv("MOCK_XRAY_STARTS", filepath.Join(t.TempDir(), "starts"))
	t.Setenv("MOCK_XRAY_CRASHES", strconv.Itoa(crashes))

	n := &tunNet{a: a, index: map[int]int{}, born: map[int]time.Time{}}
	a.interfaces = n.interfaces
	a.enableTunRouting = n.enableTunRouting
	a.disableTunRouting = n.disableTunRouting
	a.enableKillSwitch = func(system.KillSwitchConfig) error { n.event("ks on"); return nil }
	a.disableKillSwitch = func() error { n.event("ks off"); return nil }
	a.healthLoop = func(ctx context.Context, _ sessionPorts) { <-ctx.Done() }
	return a, n
}

// liveIndex is the index of the running core's interface, 0 while it has none.
func (n *tunNet) liveIndex() int {
	if n.never || n.a.runner == nil {
		return 0
	}
	pid := n.a.runner.PID()
	if !processAlive(pid) {
		return 0
	}
	if _, ok := n.index[pid]; !ok {
		n.index[pid] = 10 + len(n.index)
		n.born[pid] = time.Now()
	}
	if time.Since(n.born[pid]) < 300*time.Millisecond {
		return 0
	}
	return n.index[pid]
}

func (n *tunNet) interfaces() ([]net.Interface, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	idx := n.liveIndex()
	if idx == 0 {
		return nil, nil
	}
	return []net.Interface{{Index: idx, Name: xraycfg.TunInterfaceName, Flags: net.FlagUp}}, nil
}

func (n *tunNet) enableTunRouting(system.TunRouteConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.routes++
	if err := n.failRoute[n.routes]; err != nil {
		n.log = append(n.log, "route failed")
		return err
	}
	idx := n.liveIndex()
	if idx == 0 {
		n.log = append(n.log, "route failed")
		return errors.New(`Cannot find device "xray-tun"`)
	}
	n.log = append(n.log, fmt.Sprintf("route %d", idx))
	return nil
}

func (n *tunNet) disableTunRouting() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.unroutes++
	if err := n.failUnroute[n.unroutes]; err != nil {
		n.log = append(n.log, "unroute failed")
		return err
	}
	n.log = append(n.log, "unroute")
	return nil
}

func (n *tunNet) event(e string) {
	n.mu.Lock()
	n.log = append(n.log, e)
	n.mu.Unlock()
}

func (n *tunNet) events() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.log)
}

func (n *tunNet) routed() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.routes
}

func coreStarts(t *testing.T) int {
	t.Helper()
	b, _ := os.ReadFile(os.Getenv("MOCK_XRAY_STARTS"))
	return len(b)
}

func runTunSession(a *App, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(ctx, coreSessionTarget())
		done <- err
	}()
	return done
}

// waitFor fails the test if the session ends, or cond stays false, first.
func waitFor(t *testing.T, done <-chan error, cond func() bool, what string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case err := <-done:
			t.Fatalf("session ended before %s: %v", what, err)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func waitEnd(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("session still up")
		return nil
	}
}

// pollUntil is waitFor for a health loop, which has no test to fail.
func pollUntil(ctx context.Context, cond func() bool) bool {
	for !cond() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return true
}

// restartOnce is a health loop that asks for one restart, as three failed
// checks would.
func restartOnce(a *App) func(context.Context, sessionPorts) {
	return func(ctx context.Context, _ sessionPorts) {
		a.runner.RequestRestart()
		<-ctx.Done()
	}
}

func assertEvents(t *testing.T, n *tunNet, want ...string) {
	t.Helper()
	if got := n.events(); !slices.Equal(got, want) {
		t.Errorf("events = %q\nwant     %q", got, want)
	}
}

// The core dies; its interface and the routes on it go with it. The next core
// gets its own interface and has to be routed again — once it is up, not
// before. The kill switch belongs to the session: on once, off once.
func TestTunSession_RoutesFollowTheCore(t *testing.T) {
	a, n := newTunGenerationApp(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := runTunSession(a, ctx)
	waitFor(t, done, func() bool { return n.routed() == 2 }, "the restarted core to be routed")
	cancel()

	if err := waitEnd(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("runSession err = %v, want context.Canceled", err)
	}
	assertEvents(t, n, "route 10", "ks on", "unroute", "route 11", "unroute", "ks off")
}

// Restarts the health check asks for, three in a row, each onto a new
// interface.
func TestTunSession_RoutesEveryRequestedRestart(t *testing.T) {
	a, n := newTunGenerationApp(t, 0)
	a.healthLoop = func(ctx context.Context, _ sessionPorts) {
		for i := 1; i <= 3; i++ {
			if !pollUntil(ctx, func() bool { return n.routed() == i }) {
				return
			}
			a.runner.RequestRestart()
		}
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := runTunSession(a, ctx)
	waitFor(t, done, func() bool { return n.routed() == 4 }, "the third restart to be routed")
	cancel()

	if err := waitEnd(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("runSession err = %v, want context.Canceled", err)
	}
	assertEvents(t, n,
		"route 10", "ks on", "unroute",
		"route 11", "unroute",
		"route 12", "unroute",
		"route 13", "unroute", "ks off")
}

// Leaving while bring-up still waits for the interface is leaving, not an
// interface that failed to come up; nothing was set up, so nothing is undone.
func TestTunSession_CancelWhileWaitingForTheInterface(t *testing.T) {
	a, n := newTunGenerationApp(t, 0)
	n.never = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := runTunSession(a, ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("runSession err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end promptly on cancel")
	}
	assertEvents(t, n)
}

// A restarted core that cannot be routed — here another program took the route
// while the core was down — ends the session with the reason. It must not stay
// "connected" in front of a core with no routes to it.
func TestTunSession_SetupFailureAfterRestartEndsSession(t *testing.T) {
	a, n := newTunGenerationApp(t, 0)
	n.failRoute = map[int]error{2: errors.New("маршрут 0.0.0.0/1 уже есть в таблице")}
	a.healthLoop = restartOnce(a)

	err := waitEnd(t, runTunSession(a, context.Background()))
	if err == nil || !strings.Contains(err.Error(), "уже есть в таблице") {
		t.Fatalf("runSession err = %v, want the routing failure as the reason", err)
	}
	if n := coreStarts(t); n != 2 {
		t.Errorf("core started %d times, want 2 — no new core after a failed setup", n)
	}
	assertEvents(t, n, "route 10", "ks on", "unroute", "route failed", "ks off")
}

// Routes that would not come out after the core died end the session: a new
// core is not set up over a network in a state nobody knows. The routes stay
// owned, so the final teardown tries again.
func TestTunSession_CleanupFailureEndsSession(t *testing.T) {
	a, n := newTunGenerationApp(t, 1)
	n.failUnroute = map[int]error{1: errors.New("RTNETLINK answers: Operation not permitted")}

	err := waitEnd(t, runTunSession(a, context.Background()))
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("runSession err = %v, want the cleanup failure as the reason", err)
	}
	if n := coreStarts(t); n != 1 {
		t.Errorf("core started %d times, want 1", n)
	}
	assertEvents(t, n, "route 10", "ks on", "unroute failed", "unroute", "ks off")
	if a.tunRouted {
		t.Error("tunRouted still set after the final teardown removed the routes")
	}
}

// Routes that would not come out stay the session's: a later teardown (the one
// at exit) tries again instead of forgetting them.
func TestReleaseSession_KeepsRoutesItFailedToRemove(t *testing.T) {
	calls := 0
	a := &App{
		tunRouted: true,
		disableTunRouting: func() error {
			calls++
			if calls == 1 {
				return errors.New("RTNETLINK answers: Operation not permitted")
			}
			return nil
		},
	}

	a.releaseSession()
	if !a.tunRouted {
		t.Fatal("tunRouted dropped although the routes are still in")
	}
	a.releaseSession()
	if calls != 2 || a.tunRouted {
		t.Errorf("after a second teardown: %d removals, tunRouted = %v; want 2, false", calls, a.tunRouted)
	}
}

// The m key sets the next session's mode before this one is torn down. A core
// restarted in between still belongs to the tun session and is routed as one.
func TestTunSession_ModeSwitchDoesNotChangeTheRunningSession(t *testing.T) {
	a, n := newTunGenerationApp(t, 0)
	a.opts = Options{}
	a.noTTY = false
	a.connectScreen = func(_ string, connect func() error, _ func()) error { return connect() }
	a.showStatus = func(tui.StatusInfo, <-chan tui.StatusUpdate) (tui.StatusAction, error) {
		a.mode = "proxy"
		a.runner.RequestRestart()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pollUntil(ctx, func() bool { return n.routed() == 2 })
		return tui.StatusBack, nil
	}

	action, err := a.runSession(context.Background(), coreSessionTarget())
	if err != nil || action != tui.StatusBack {
		t.Fatalf("runSession = %v, %v; want StatusBack", action, err)
	}
	assertEvents(t, n, "route 10", "ks on", "unroute", "route 11", "unroute", "ks off")
}

// The config binds direct traffic to the adapter that was physical when it was
// built. When a restart finds another one, routing the new core would send that
// traffic out a path that is gone: the session ends and says to reconnect.
func TestTunSession_RestartOnAnotherAdapterEndsSession(t *testing.T) {
	a, n := newTunGenerationApp(t, 0)
	adapters := []string{"Ethernet", "Wi-Fi"}
	calls := 0
	a.directBind = func() (xraycfg.DirectBind, error) {
		name := adapters[min(calls, 1)]
		calls++
		return xraycfg.DirectBind{Interface: name}, nil
	}
	a.healthLoop = restartOnce(a)

	err := waitEnd(t, runTunSession(a, context.Background()))
	if err == nil || !strings.Contains(err.Error(), "Wi-Fi") {
		t.Fatalf("runSession err = %v, want the adapter change as the reason", err)
	}
	assertEvents(t, n, "route 10", "ks on", "unroute", "ks off")
}
