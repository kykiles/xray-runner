package subscription

import (
	"net/http"
	"net/http/httptest"
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

	_, _ = FetchWithHWID(server.URL, "test-hwid-123", "windows", "xray-runner")

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

	_, _ = Fetch(server.URL)

	// Some subscription panels return an empty body unless a recognized client
	// User-Agent is sent; the Go default ("Go-http-client/...") must not leak.
	if ua == "" || len(ua) < 5 || ua[:2] == "Go" {
		t.Errorf("User-Agent = %q, want a recognized subscription client UA", ua)
	}
}

func TestFetchWithHWID_ParsesSubscription(t *testing.T) {
	encodedSub := "dmxlc3M6Ly91dWlkQGhvc3Q6NDQzP3R5cGU9dGNwJnNlY3VyaXR5PXJlbGl0eQ=="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(encodedSub))
	}))
	defer server.Close()

	entries, err := FetchWithHWID(server.URL, "test-hwid", "linux", "xray-runner")
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

	_, err := FetchWithHWID(server.URL, "hwid", "linux", "xray-runner")
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestFetchWithHWID_TransportError(t *testing.T) {
	_, err := FetchWithHWID("http://127.0.0.1:1", "hwid", "linux", "xray-runner")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}
