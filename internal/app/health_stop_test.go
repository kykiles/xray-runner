package app

// The health loop reads a.runner; teardown writes it. Stopping the session must
// therefore wait for the loop to be gone, not merely ask it to stop — otherwise
// a loop still in flight races the teardown and can request a restart on the
// next session's core.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartHealthStopWaitsForTheLoop(t *testing.T) {
	var finished atomic.Bool
	a := &App{
		healthLoop: func(ctx context.Context, _ sessionPorts) {
			<-ctx.Done()
			// A real loop can still be inside a dial when the context ends.
			time.Sleep(50 * time.Millisecond)
			finished.Store(true)
		},
	}

	stop := a.startHealth(context.Background(), sessionPorts{})
	stop()

	if !finished.Load() {
		t.Error("stop() returned while the health loop was still running")
	}
}

// Both the teardown and the screen-closer goroutine call stop; the second call
// must be harmless.
func TestStartHealthStopIsIdempotent(t *testing.T) {
	var calls atomic.Int32
	a := &App{
		healthLoop: func(ctx context.Context, _ sessionPorts) {
			calls.Add(1)
			<-ctx.Done()
		},
	}

	stop := a.startHealth(context.Background(), sessionPorts{})
	stop()
	stop()

	if got := calls.Load(); got != 1 {
		t.Errorf("health loop ran %d times, want 1", got)
	}
}

// The loop must also end when the session context ends on its own (core died,
// Ctrl+C), without anybody calling stop.
func TestStartHealthStopsWithTheSessionContext(t *testing.T) {
	done := make(chan struct{})
	a := &App{
		healthLoop: func(ctx context.Context, _ sessionPorts) {
			<-ctx.Done()
			close(done)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := a.startHealth(ctx, sessionPorts{})
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health loop did not stop when the session context was cancelled")
	}
	stop()
}
