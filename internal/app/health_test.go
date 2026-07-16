package app

import (
	"context"
	"testing"
	"time"
)

func TestParseCoreVersion(t *testing.T) {
	cases := map[string]string{
		"Xray 26.6.27 (Xray, Penetrates Everything.) 45cf289 (go1.26.4 linux/amd64)": "26.6.27",
		"Xray 1.8.4":     "1.8.4",
		"":               "",
		"Xray":           "",
		"  Xray 2.0.0  ": "2.0.0",
	}
	for line, want := range cases {
		if got := parseCoreVersion(line); got != want {
			t.Errorf("parseCoreVersion(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestConnectivityWarmup_SucceedsFirstTry(t *testing.T) {
	calls := 0
	probe := func() (bool, time.Duration) {
		calls++
		return true, 5 * time.Millisecond
	}
	ok, lat := connectivityWarmup(context.Background(), probe, time.Second, 10*time.Millisecond)
	if !ok {
		t.Fatal("expected ok on the first successful probe")
	}
	if calls != 1 {
		t.Errorf("a first-try success must not retry, got %d calls", calls)
	}
	if lat != 5*time.Millisecond {
		t.Errorf("latency = %v, want 5ms", lat)
	}
}

func TestConnectivityWarmup_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	probe := func() (bool, time.Duration) {
		calls++
		return calls >= 3, 0 // cold for the first two tries, then up
	}
	ok, _ := connectivityWarmup(context.Background(), probe, time.Second, time.Millisecond)
	if !ok {
		t.Fatal("expected the warm-up to ride out a cold start and succeed")
	}
	if calls != 3 {
		t.Errorf("expected 3 probes before success, got %d", calls)
	}
}

func TestConnectivityWarmup_FailsAfterWindow(t *testing.T) {
	probe := func() (bool, time.Duration) { return false, 0 }
	ok, _ := connectivityWarmup(context.Background(), probe, 20*time.Millisecond, 5*time.Millisecond)
	if ok {
		t.Fatal("a permanently-down probe must report failure once the window elapses")
	}
}

func TestConnectivityWarmup_ContextCancelled(t *testing.T) {
	probe := func() (bool, time.Duration) { return false, 0 }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	ok, _ := connectivityWarmup(ctx, probe, time.Minute, time.Second)
	if ok {
		t.Fatal("cancelled context must not report success")
	}
	if time.Since(start) > time.Second {
		t.Error("cancellation should abort the warm-up promptly, not sleep out the window")
	}
}
