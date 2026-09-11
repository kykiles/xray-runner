package subscription

// A08: a plain-http subscription sent the token and the device headers in the
// clear with only a log line to say so, and nothing could stop a fetch short of
// its 15-second timeout.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("no network in this test")
}

func TestFetchBody_RefusesPlainHTTP(t *testing.T) {
	tr := &countingTransport{}
	orig := fetchTransport
	fetchTransport = tr
	t.Cleanup(func() { fetchTransport = orig })

	_, _, err := fetchBody(context.Background(), "http://example.com/sub/SECRETTOKEN?x=SECRET2")
	if err == nil {
		t.Fatal("plain-http subscription accepted")
	}
	if n := tr.calls.Load(); n != 0 {
		t.Errorf("%d request(s) went out before the refusal", n)
	}
	assertNoToken(t, "refusal", err.Error())
	if !strings.Contains(err.Error(), "http://") {
		t.Errorf("refusal %q does not say why", err)
	}
}

// Tests and a panel on the same machine talk over loopback, where there is no
// wire for the token to cross.
func TestFetchBody_AllowsLoopbackHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body, _, err := fetchBody(context.Background(), srv.URL+"/sub")
	if err != nil || string(body) != "ok" {
		t.Fatalf("loopback http: body=%q err=%v", body, err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":     true,
		"127.0.0.1":     true,
		"127.3.4.5":     true,
		"::1":           true,
		"example.com":   false,
		"10.0.0.1":      false,
		"127.evil.com":  false,
		"localhost.com": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestFetchBody_CanceledContextAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, _, err := fetchBody(ctx, srv.URL+"/sub")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("fetch took %v after cancel, want it to stop at once", elapsed)
	}
}
