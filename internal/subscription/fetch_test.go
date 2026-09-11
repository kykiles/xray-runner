package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchWithHWID_SetsHeaders(t *testing.T) {
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(""))
	}))
	defer server.Close()

	_, _ = FetchWithHWID(context.Background(), server.URL, "test-hwid-123", "windows", "xray-runner")

	if capturedHeaders == nil {
		t.Fatal("no headers captured")
	}
	if got := capturedHeaders.Get("x-hwid"); got != "test-hwid-123" {
		t.Errorf("x-hwid = %q, want %q", got, "test-hwid-123")
	}
	if got := capturedHeaders.Get("x-device-os"); got != "windows" {
		t.Errorf("x-device-os = %q, want %q", got, "windows")
	}
	if got := capturedHeaders.Get("x-device-model"); got != "xray-runner" {
		t.Errorf("x-device-model = %q, want %q", got, "xray-runner")
	}
}

func TestFetch_SetsClientUserAgent(t *testing.T) {
	var ua string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, _ = Fetch(context.Background(), server.URL)

	// Some subscription panels return an empty body unless a recognized client
	// User-Agent is sent; the Go default ("Go-http-client/...") must not leak.
	if ua == "" || len(ua) < 5 || ua[:2] == "Go" {
		t.Errorf("User-Agent = %q, want a recognized subscription client UA", ua)
	}
}

func TestFetch_RetriesWithNextUserAgent(t *testing.T) {
	// A panel that has no template for the first UA answers 502 (real behaviour
	// seen on a Remnawave sub) — the fetch must fall back to the next client.
	encodedSub := "dmxlc3M6Ly91dWlkQGhvc3Q6NDQzP3R5cGU9dGNwJnNlY3VyaXR5PXJlbGl0eQ=="
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		seen = append(seen, ua)
		if ua == userAgents[0] {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(encodedSub))
	}))
	defer server.Close()

	entries, err := Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if len(seen) != 2 || seen[1] != userAgents[1] {
		t.Errorf("user agents tried = %v, want both", seen)
	}
}

func TestFetchWithHWID_ParsesSubscription(t *testing.T) {
	encodedSub := "dmxlc3M6Ly91dWlkQGhvc3Q6NDQzP3R5cGU9dGNwJnNlY3VyaXR5PXJlbGl0eQ=="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(encodedSub))
	}))
	defer server.Close()

	entries, err := FetchWithHWID(context.Background(), server.URL, "test-hwid", "linux", "xray-runner")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Protocol != "vless" {
		t.Errorf("protocol = %q, want %q", entries[0].Protocol, "vless")
	}
	if entries[0].Address != "host" {
		t.Errorf("address = %q, want %q", entries[0].Address, "host")
	}
}

func TestFetchWithHWID_404Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := FetchWithHWID(context.Background(), server.URL, "hwid", "linux", "xray-runner")
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestFetchWithHWID_TransportError(t *testing.T) {
	_, err := FetchWithHWID(context.Background(), "http://127.0.0.1:1", "hwid", "linux", "xray-runner")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestFetch_RejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Stream just over the cap so the reader trips the limit.
		chunk := make([]byte, 1<<20)
		for written := 0; written <= maxSubscriptionBody; written += len(chunk) {
			w.Write(chunk)
		}
	}))
	defer server.Close()

	if _, err := Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("expected error for a body over the size cap")
	}
}

// A subscription served over https must not be followed into plain http: the
// token in the URL and the device headers would go out in the clear, and the
// plaintext warning only ever looked at the original URL. The policy is tested
// directly — driving it through a real TLS server would need the client to
// trust a test certificate.
func TestCheckRedirect_RefusesHTTPSDowngrade(t *testing.T) {
	from, _ := http.NewRequest("GET", "https://panel.example.com/sub", nil)
	to, _ := http.NewRequest("GET", "http://panel.example.com/sub", nil)

	err := checkRedirect(to, []*http.Request{from})
	if err == nil {
		t.Fatal("expected https->http redirect to be refused, got nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q does not mention the downgrade", err)
	}
}

// An https->https redirect is ordinary and must be allowed.
func TestCheckRedirect_AllowsSameSchemeRedirect(t *testing.T) {
	from, _ := http.NewRequest("GET", "https://panel.example.com/sub", nil)
	to, _ := http.NewRequest("GET", "https://cdn.example.com/sub", nil)

	if err := checkRedirect(to, []*http.Request{from}); err != nil {
		t.Errorf("checkRedirect: %v", err)
	}
}

// Supplying CheckRedirect drops Go's own 10-redirect cap, so a panel looping
// redirects would be chased until the client timeout.
func TestCheckRedirect_StopsAfterTenHops(t *testing.T) {
	from, _ := http.NewRequest("GET", "https://panel.example.com/sub", nil)
	to, _ := http.NewRequest("GET", "https://panel.example.com/sub", nil)

	via := make([]*http.Request, 9)
	for i := range via {
		via[i] = from
	}
	if err := checkRedirect(to, via); err != nil {
		t.Fatalf("9 hops must still be allowed: %v", err)
	}
	if err := checkRedirect(to, append(via, from)); err == nil {
		t.Error("10 hops returned no error")
	}
}

// x-hwid identifies the user's device. Go strips only Authorization/Cookie on a
// cross-host redirect, so the HWID headers would otherwise reach a third party.
func TestFetchBody_DropsHWIDHeadersOnCrossHostRedirect(t *testing.T) {
	var got http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("ok"))
	}))
	defer other.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	if _, _, err := fetchBody(context.Background(), origin.URL, WithHWID("dev-hwid", "linux", "pc")); err != nil {
		t.Fatalf("fetchBody: %v", err)
	}
	for _, h := range []string{"X-Hwid", "X-Device-Os", "X-Device-Model"} {
		if v := got.Get(h); v != "" {
			t.Errorf("header %s leaked to other host: %q", h, v)
		}
	}
}

// Same-host redirects are ordinary panel behavior and must keep working.
func TestFetchBody_KeepsHWIDHeadersOnSameHostRedirect(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/final" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		got = r.Header.Clone()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, _, err := fetchBody(context.Background(), srv.URL, WithHWID("dev-hwid", "linux", "pc")); err != nil {
		t.Fatalf("fetchBody: %v", err)
	}
	if got.Get("X-Hwid") != "dev-hwid" {
		t.Errorf("X-Hwid = %q, want dev-hwid", got.Get("X-Hwid"))
	}
}
