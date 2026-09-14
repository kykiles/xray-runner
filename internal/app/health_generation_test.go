package app

// C01: a health result belongs to the core that produced it. Once that core has
// stopped, its results must not reach the screen, and the reset that says so
// must reach it even when the screen is behind on its updates.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xray-runner/internal/tui"
)

// A screen that has not caught up must not lose the reset: the stopped core's
// "ок" would then stay on it until the next core's first check.
func TestResetHealth_MakesRoomInAFullChannel(t *testing.T) {
	a := &App{}
	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)
	for range cap(ch) {
		ch <- tui.StatusUpdate{OK: true, Generation: 1}
	}

	returned := make(chan struct{})
	go func() {
		a.resetHealth(2)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("resetHealth blocked on a full channel")
	}

	if n := len(ch); n != cap(ch) {
		t.Errorf("%d updates queued, want %d — one old update makes room, no more", n, cap(ch))
	}
	var last tui.StatusUpdate
	for len(ch) > 0 {
		last = <-ch
	}
	if !last.ResetHealth || last.Generation != 2 {
		t.Errorf("last update = %+v, want the reset to core 2", last)
	}
}

func TestRecordHealth_DropsTheStoppedCore(t *testing.T) {
	a := &App{healthGen: 1}
	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)

	a.recordHealth(1, true, 30*time.Millisecond)
	a.resetHealth(2)
	if !a.lastCheck.IsZero() || a.lastCheckOK {
		t.Errorf("after the reset lastCheck = %v, ok = %v; want both cleared", a.lastCheck, a.lastCheckOK)
	}
	a.recordHealth(1, true, 30*time.Millisecond) // the stopped core, late
	a.recordHealth(2, true, 30*time.Millisecond)

	var got []tui.StatusUpdate
	for len(ch) > 0 {
		got = append(got, <-ch)
	}
	want := []tui.StatusUpdate{
		{OK: true, Latency: 30 * time.Millisecond, Generation: 1},
		{ResetHealth: true, Generation: 2},
		{OK: true, Latency: 30 * time.Millisecond, Generation: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].OK != want[i].OK || got[i].ResetHealth != want[i].ResetHealth || got[i].Generation != want[i].Generation {
			t.Errorf("update %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The check of the generation and the send are one step: a result of core 1
// that was being recorded while the core stopped lands before the reset or not
// at all.
func TestRecordHealth_NothingOfTheStoppedCoreAfterItsReset(t *testing.T) {
	a := &App{healthGen: 1}
	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)

	var reset, stale atomic.Bool
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for u := range ch {
			switch {
			case u.ResetHealth:
				reset.Store(true)
			case reset.Load() && u.Generation == 1:
				stale.Store(true)
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				a.recordHealth(1, true, time.Millisecond)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	a.resetHealth(2)
	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()
	a.closeStatusCh(ch)
	<-drained

	if !reset.Load() {
		t.Fatal("the reset never reached the screen")
	}
	if stale.Load() {
		t.Error("a result of the stopped core reached the screen after its reset")
	}
}

// No screen (a headless run), or one the session has already taken down: the
// reset and a late result go nowhere, without a panic or a block.
func TestResetHealth_WithoutAScreen(t *testing.T) {
	a := &App{}
	a.resetHealth(2)
	a.recordHealth(2, true, time.Millisecond)

	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)
	a.closeStatusCh(ch)
	a.resetHealth(3)
	a.recordHealth(3, true, time.Millisecond)
}
