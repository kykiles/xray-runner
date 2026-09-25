package xray

// RunWithRetryHooks brackets every core process with the session's setup and
// cleanup (C01): a restarted core gets a new TUN interface, and routes pinned
// to the old one are gone with it. These tests hold the lifecycle contract the
// session will build on.

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookLog records hook calls in order; hooks run on the runner's goroutine
// while the test may read it.
type hookLog struct {
	mu     sync.Mutex
	events []string
}

func (l *hookLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *hookLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// writeConfig is the config the mock cores are started with; they ignore it.
func writeConfig(t *testing.T) []byte {
	t.Helper()
	return []byte("{}")
}

// assertReaped fails when pid is still a process, running or a zombie nobody
// waited for. The state comes from /proc, so elsewhere it checks nothing.
func assertReaped(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS != "linux" || pid == 0 {
		return
	}
	if state, ok := procState(pid); ok {
		t.Errorf("pid %d is still there (state %s)", pid, state)
	}
}

// runAsync runs RunWithRetryHooks on its own goroutine, as the session does.
func runAsync(r *Runner, ctx context.Context, maxRetries int, hooks RunHooks) <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.RunWithRetryHooks(ctx, maxRetries, hooks) }()
	return done
}

func waitDone(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("RunWithRetryHooks did not return within %v", within)
		return nil
	}
}

func signalOn(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("hook was not reached")
	}
}

// Every core that came up gets its setup while it runs and its cleanup once it
// is gone, before the next one starts: the cleanup sees the old process reaped
// and no new one yet, and the setup's context has ended with the core.
func TestRunWithRetryHooksRunAroundEveryCore(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("XRAY_COUNTER", counter)
	r := New(buildCountingXray(t, 2100*time.Millisecond), writeConfig(t))

	var log hookLog
	var pid int
	var genCtx context.Context
	hooks := RunHooks{
		AfterStart: func(ctx context.Context) error {
			// A hook may call the runner: it is not run under r.mu.
			pid, genCtx = r.PID(), ctx
			if state, ok := procState(pid); runtime.GOOS == "linux" && (!ok || state == "Z") {
				log.add("start without a live core")
				return nil
			}
			log.add("start")
			return nil
		},
		AfterStop: func() error {
			switch {
			case genCtx.Err() == nil:
				log.add("stop with the setup context still live")
			case r.PID() != pid:
				log.add("stop after the next core started")
			default:
				log.add("stop")
			}
			assertReaped(t, pid)
			return nil
		},
	}

	err := r.RunWithRetryHooks(context.Background(), 2, hooks)
	if err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Fatalf("RunWithRetryHooks = %v, want the exhausted-budget error", err)
	}
	if want := []string{"start", "stop", "start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	if n := startCount(t, counter); n != 2 {
		t.Errorf("core started %d times, want 2", n)
	}
}

// Cancelling the session stops the core, and cleanup runs exactly once.
func TestRunWithRetryHooksCancelStopsOnce(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	var log hookLog
	var pid int
	started := make(chan struct{}, 1)
	hooks := RunHooks{
		AfterStart: func(context.Context) error { pid = r.PID(); log.add("start"); signalOn(started); return nil },
		AfterStop:  func() error { log.add("stop"); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 5, hooks)
	waitSignal(t, started)
	cancel()
	if err := waitDone(t, done, 5*time.Second); err != nil {
		t.Fatalf("RunWithRetryHooks after cancel = %v, want nil", err)
	}
	if want := []string{"start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	assertReaped(t, pid)
}

// A requested restart is a new generation like any other: cleanup for the old
// core, setup for the new one.
func TestRunWithRetryHooksRequestRestart(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	var log hookLog
	started := make(chan struct{}, 1)
	hooks := RunHooks{
		AfterStart: func(context.Context) error { log.add("start"); signalOn(started); return nil },
		AfterStop:  func() error { log.add("stop"); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 5, hooks)
	waitSignal(t, started)
	r.RequestRestart()
	waitSignal(t, started)
	cancel()
	if err := waitDone(t, done, 5*time.Second); err != nil {
		t.Fatalf("RunWithRetryHooks after cancel = %v, want nil", err)
	}
	if want := []string{"start", "stop", "start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
}

// An almost-immediate exit stays fatal, and what the setup did is still undone.
func TestRunWithRetryHooksImmediateExit(t *testing.T) {
	r := New(buildMockXray(t, 1, 50*time.Millisecond), writeConfig(t))

	var log hookLog
	hooks := RunHooks{
		AfterStart: func(context.Context) error { log.add("start"); return nil },
		AfterStop:  func() error { log.add("stop"); return nil },
	}

	err := r.RunWithRetryHooks(context.Background(), 5, hooks)
	if err == nil || !strings.Contains(err.Error(), "exited immediately") {
		t.Fatalf("RunWithRetryHooks = %v, want the immediate-exit error", err)
	}
	if want := []string{"start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
}

// No process, nothing to set up or clean after.
func TestRunWithRetryHooksStartErrorSkipsHooks(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "no-such-xray"), writeConfig(t))

	var log hookLog
	hooks := RunHooks{
		AfterStart: func(context.Context) error { log.add("start"); return nil },
		AfterStop:  func() error { log.add("stop"); return nil },
	}

	if err := r.RunWithRetryHooks(context.Background(), 5, hooks); err == nil {
		t.Fatal("RunWithRetryHooks = nil for a core that cannot start")
	}
	if got := log.get(); len(got) != 0 {
		t.Errorf("hooks = %q for a core that never started, want none", got)
	}
}

// Setup failing while the core runs (a route that would not go in) ends the
// run with that error: the core is stopped and reaped, the partial setup is
// undone, and no second core is started for a retry that cannot help.
func TestRunWithRetryHooksSetupErrorStopsTheCore(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	errSetup := errors.New("route add failed")
	var log hookLog
	var pid int
	hooks := RunHooks{
		AfterStart: func(context.Context) error { pid = r.PID(); log.add("start"); return errSetup },
		AfterStop:  func() error { log.add("stop"); assertReaped(t, pid); return nil },
	}

	base := runtime.NumGoroutine()
	done := runAsync(r, context.Background(), 5, hooks)
	// The mock stays up for 10s: returning sooner means the runner stopped it.
	err := waitDone(t, done, 5*time.Second)
	if !errors.Is(err, errSetup) {
		t.Fatalf("RunWithRetryHooks = %v, want the setup error", err)
	}
	// One start, one stop: a setup error is not a crash to retry.
	if want := []string{"start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	assertReaped(t, pid)
	// Nothing is left behind, the goroutine waiting for the core included.
	waitFor(t, func() bool { return runtime.NumGoroutine() <= base })
}

// The core dying under a setup still in progress is a crash, not a setup
// failure: the setup's context ends, and the usual restart policy applies —
// whether the setup reports the cancelled context or the symptom it ran into.
func TestRunWithRetryHooksCoreDiesDuringSetup(t *testing.T) {
	cases := map[string]func(ctx context.Context) error{
		"setup returns the context error": func(ctx context.Context) error { return ctx.Err() },
		"setup returns the symptom":       func(context.Context) error { return errors.New("Cannot find device \"xray-tun\"") },
	}
	for name, symptom := range cases {
		t.Run(name, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "count")
			t.Setenv("XRAY_COUNTER", counter)
			// Up for 2.1s — past the immediate-exit cutoff, so it is a crash.
			r := New(buildCountingXray(t, 2100*time.Millisecond), writeConfig(t))

			var log hookLog
			second := make(chan struct{}, 1)
			gen := 0
			hooks := RunHooks{
				AfterStart: func(ctx context.Context) error {
					gen++
					if gen == 1 {
						<-ctx.Done()
						log.add("setup cut short")
						return symptom(ctx)
					}
					log.add("start")
					signalOn(second)
					return nil
				},
				AfterStop: func() error { log.add("stop"); return nil },
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := runAsync(r, ctx, 5, hooks)
			waitSignal(t, second)
			cancel()
			if err := waitDone(t, done, 5*time.Second); err != nil {
				t.Fatalf("RunWithRetryHooks = %v, want the core restarted and a clean cancel", err)
			}
			if want := []string{"setup cut short", "stop", "start", "stop"}; !slices.Equal(log.get(), want) {
				t.Errorf("hooks = %q, want %q", log.get(), want)
			}
		})
	}
}

// A restart asked for while the setup is still running is a restart, not a
// crash: with a budget of one, a crash would end the run.
func TestRunWithRetryHooksRestartDuringSetup(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	var log hookLog
	inSetup := make(chan struct{}, 1)
	second := make(chan struct{}, 1)
	gen := 0
	hooks := RunHooks{
		AfterStart: func(ctx context.Context) error {
			gen++
			if gen == 1 {
				signalOn(inSetup)
				<-ctx.Done()
				log.add("setup cut short")
				return ctx.Err()
			}
			log.add("start")
			signalOn(second)
			return nil
		},
		AfterStop: func() error { log.add("stop"); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 1, hooks)
	waitSignal(t, inSetup)
	r.RequestRestart()
	waitSignal(t, second)
	cancel()
	if err := waitDone(t, done, 5*time.Second); err != nil {
		t.Fatalf("RunWithRetryHooks = %v, want the requested restart and a clean cancel", err)
	}
	if want := []string{"setup cut short", "stop", "start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
}

// A cleanup that failed leaves the network in a state nobody knows; a new core
// is not started on top of it, whatever budget is left.
func TestRunWithRetryHooksCleanupErrorIsFinal(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("XRAY_COUNTER", counter)
	r := New(buildCountingXray(t, 2100*time.Millisecond), writeConfig(t))

	errCleanup := errors.New("route del failed")
	var log hookLog
	hooks := RunHooks{
		AfterStart: func(context.Context) error { log.add("start"); return nil },
		AfterStop:  func() error { log.add("stop"); return errCleanup },
	}

	err := waitDone(t, runAsync(r, context.Background(), 5, hooks), 10*time.Second)
	if !errors.Is(err, errCleanup) {
		t.Fatalf("RunWithRetryHooks = %v, want the cleanup error", err)
	}
	if want := []string{"start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	if n := startCount(t, counter); n != 1 {
		t.Errorf("core started %d times after a failed cleanup, want 1", n)
	}
}

// The cleanup failing after the user left is still reported.
func TestRunWithRetryHooksCleanupErrorOnCancel(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	errCleanup := errors.New("route del failed")
	started := make(chan struct{}, 1)
	hooks := RunHooks{
		AfterStart: func(context.Context) error { signalOn(started); return nil },
		AfterStop:  func() error { return errCleanup },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 5, hooks)
	waitSignal(t, started)
	cancel()
	if err := waitDone(t, done, 5*time.Second); !errors.Is(err, errCleanup) {
		t.Fatalf("RunWithRetryHooks = %v, want the cleanup error", err)
	}
}

// Leaving while the setup waits (say, for the TUN interface) is a clean exit:
// the setup's context ends, the core is stopped, cleanup runs once.
func TestRunWithRetryHooksCancelDuringSetup(t *testing.T) {
	r := New(buildSignalXray(t), writeConfig(t))

	var log hookLog
	var pid int
	inSetup := make(chan struct{}, 1)
	hooks := RunHooks{
		AfterStart: func(ctx context.Context) error {
			pid = r.PID()
			signalOn(inSetup)
			<-ctx.Done()
			log.add("setup cut short")
			return ctx.Err()
		},
		AfterStop: func() error { log.add("stop"); return nil },
	}

	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 5, hooks)
	waitSignal(t, inSetup)
	cancel()
	if err := waitDone(t, done, 5*time.Second); err != nil {
		t.Fatalf("RunWithRetryHooks = %v, want nil for a cancelled session", err)
	}
	if want := []string{"setup cut short", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	assertReaped(t, pid)
	waitFor(t, func() bool { return runtime.NumGoroutine() <= base })
}

// Leaving during the backoff after a crash returns at once, without another
// core or another cleanup.
func TestRunWithRetryHooksCancelDuringBackoff(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("XRAY_COUNTER", counter)
	r := New(buildCountingXray(t, 2100*time.Millisecond), writeConfig(t))

	var log hookLog
	stopped := make(chan struct{}, 1)
	hooks := RunHooks{
		AfterStart: func(context.Context) error { log.add("start"); return nil },
		AfterStop:  func() error { log.add("stop"); signalOn(stopped); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(r, ctx, 5, hooks)
	waitSignal(t, stopped)
	cancel()
	// The first backoff is a second long.
	if err := waitDone(t, done, 500*time.Millisecond); err != nil {
		t.Fatalf("RunWithRetryHooks = %v, want nil for a cancelled session", err)
	}
	if want := []string{"start", "stop"}; !slices.Equal(log.get(), want) {
		t.Errorf("hooks = %q, want %q", log.get(), want)
	}
	if n := startCount(t, counter); n != 1 {
		t.Errorf("core started %d times, want 1", n)
	}
}
