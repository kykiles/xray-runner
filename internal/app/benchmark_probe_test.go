package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// rewriteHost sends every request to the test server, standing in for the proxy
// transport the real benchmark uses.
type rewriteHost struct{ to *url.URL }

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// A balancer whose observatory has no data yet blackholes the first requests.
// The probe must keep trying instead of reporting a timeout.
func TestProbeRetriesUntilBalancerIsReady(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	const checkURL = "https://example.invalid/generate_204"
	base, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: rewriteHost{base}, Timeout: 5 * time.Second}

	got := probe(context.Background(), client, checkURL, time.Now().Add(5*time.Second))
	if got.Error != nil {
		t.Fatalf("probe: %v", got.Error)
	}
	// Two blackholed attempts, the one that gets through, and the warm-up repeat
	// whose timing is the number actually reported.
	if n != 4 {
		t.Errorf("attempts = %d, want 4", n)
	}

	// A permanently dead endpoint still ends as a failure, not a hang.
	srv.Close()
	if dead := probe(context.Background(), client, checkURL, time.Now().Add(500*time.Millisecond)); dead.Error == nil {
		t.Errorf("want error for always-failing endpoint, got %v", dead.Latency)
	}
}

// The reported number is the warm request, not the cold one that opened the
// connection — otherwise the benchmark and the connected screen measure two
// different things. If the warm repeat fails, the cold timing stands.
func TestProbeReportsWarmRequest(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			time.Sleep(150 * time.Millisecond) // the cold handshake
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: rewriteHost{base}, Timeout: 5 * time.Second}

	got := probe(context.Background(), client, "https://example.invalid/generate_204", time.Now().Add(5*time.Second))
	if got.Error != nil {
		t.Fatalf("probe: %v", got.Error)
	}
	if got.Latency >= 150*time.Millisecond {
		t.Errorf("latency = %v, want the warm request, not the cold one", got.Latency)
	}
}
