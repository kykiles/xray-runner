package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// Every way the updater reaches the network — the API, the checksum files and
// the artifacts, across both clients — holds the first URL and each redirect to
// one rule: https anywhere, plain http only to loopback, no step down from
// https, a bounded number of hops (E02).
func TestTransportPolicy_OnEveryRequest(t *testing.T) {
	var tlsA, tlsB, plain *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to := map[string]string{
			"/to-http":          plain.URL + "/ok",
			"/to-https":         tlsA.URL + "/ok",
			"/to-other-https":   tlsB.URL + "/ok",
			"/to-external-http": "http://192.0.2.1/ok",
			"/to-ftp":           "ftp://127.0.0.1/ok",
			"/loop":             "/loop",
		}[r.URL.Path]
		if to != "" {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	tlsA, tlsB, plain = httptest.NewTLSServer(handler), httptest.NewTLSServer(handler), httptest.NewServer(handler)
	defer tlsA.Close()
	defer tlsB.Close()
	defer plain.Close()

	cases := []struct {
		name string
		url  string
		ok   bool
		sent []string // origins the client sent a request to, in order
	}{
		{"https", tlsA.URL + "/ok", true, []string{tlsA.URL}},
		{"https to another https origin", tlsA.URL + "/to-other-https", true, []string{tlsA.URL, tlsB.URL}},
		{"loopback http", plain.URL + "/ok", true, []string{plain.URL}},
		{"loopback http to https", plain.URL + "/to-https", true, []string{plain.URL, tlsA.URL}},
		{"https down to loopback http", tlsA.URL + "/to-http", false, []string{tlsA.URL}},
		{"external http", "http://192.0.2.1/ok", false, nil},
		{"loopback http to external http", plain.URL + "/to-external-http", false, []string{plain.URL}},
		{"other scheme", "ftp://127.0.0.1/ok", false, nil},
		{"redirect to another scheme", plain.URL + "/to-ftp", false, []string{plain.URL}},
		{"redirect loop", plain.URL + "/loop", false, slices.Repeat([]string{plain.URL}, 10)},
	}

	for fname, fetch := range fetchers {
		for _, c := range cases {
			t.Run(fname+"/"+c.name, func(t *testing.T) {
				rt := &recordingTransport{next: tlsA.Client().Transport}
				testTransport = rt
				t.Cleanup(func() { testTransport = nil })

				err := fetch(t, c.url)
				if c.ok && err != nil {
					t.Fatalf("refused: %v", err)
				}
				if !c.ok && err == nil {
					t.Fatal("accepted")
				}
				if !slices.Equal(rt.sent, c.sent) {
					t.Errorf("sent to %q, want %q", rt.sent, c.sent)
				}
			})
		}
	}
}

// Go puts the previous URL — path and query, a panel's token with them — in the
// Referer of each redirect. The updater sends it to no one.
func TestRedirect_DropsReferer(t *testing.T) {
	var mu sync.Mutex
	var seen []string // Referer of each request the redirect target got
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Referer"))
		mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/ok", http.StatusFound)
	}))
	defer origin.Close()
	trustTLS(t, origin)
	url := origin.URL + "/" + secretToken + "/geoip.dat?token=" + secretToken

	for name, fetch := range fetchers {
		t.Run(name, func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()
			if err := fetch(t, url); err != nil {
				t.Fatalf("redirect not followed: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) != 1 || seen[0] != "" {
				t.Errorf("redirect target got Referer %q, want none", seen)
			}
		})
	}
}

// fetchers are the updater's three ways onto the network, one per kind of
// request: the API, a checksum file, an artifact.
var fetchers = map[string]func(t *testing.T, url string) error{
	"api": func(t *testing.T, url string) error {
		var v any
		return getJSON(context.Background(), url, &v)
	},
	"checksum": func(t *testing.T, url string) error {
		_, err := httpGetBytes(context.Background(), url)
		return err
	},
	"artifact": func(t *testing.T, url string) error {
		f, err := download(context.Background(), Asset{Name: "artifact", URL: url}, t.TempDir())
		if err == nil {
			discard(f)
		}
		return err
	},
}

// recordingTransport notes the origin of every request the client sends and
// lets none leave the machine or use a scheme other than http(s): a request the
// policy should have refused shows up in sent instead of on the network.
type recordingTransport struct {
	next http.RoundTripper
	mu   sync.Mutex
	sent []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.sent = append(rt.sent, req.URL.Scheme+"://"+req.URL.Host)
	rt.mu.Unlock()
	if req.URL.Hostname() != "127.0.0.1" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return nil, errors.New("test transport: request refused")
	}
	return rt.next.RoundTrip(req)
}
