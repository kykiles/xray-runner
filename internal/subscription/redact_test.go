package subscription

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A04: the subscription token lives in the URL's path or query, and a
// *url.Error prints the whole URL — so the token reached the log and the screen
// every time a fetch failed.
const secretPath = "/sub/SECRETTOKEN?x=SECRET2"

func assertNoToken(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, "SECRETTOKEN") || strings.Contains(s, "SECRET2") {
		t.Errorf("%s leaks the token: %s", what, s)
	}
}

func TestFetchBody_ErrorsHideToken(t *testing.T) {
	closer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer closer.Close()

	cases := []struct {
		name, url, host string
	}{
		{"connection closed", closer.URL + secretPath, strings.TrimPrefix(closer.URL, "http://")},
		{"no such host", "https://nonexistent.invalid" + secretPath, "nonexistent.invalid"},
		// Rejected by url.Parse before any request: nothing reliable to show.
		{"unparseable", "http://127.0.0.1:1/sub/SECRETTOKEN\x7f?x=SECRET2", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := fetchBody(c.url)
			if err == nil {
				t.Fatal("fetchBody succeeded")
			}
			assertNoToken(t, "error", err.Error())
			if c.host != "" && !strings.Contains(err.Error(), c.host) {
				t.Errorf("error %q lost the host %s", err, c.host)
			}
		})
	}
}

func TestFetchBody_HTTPWarningHidesToken(t *testing.T) {
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	_, _, _ = fetchBody("http://127.0.0.1:1" + secretPath)

	assertNoToken(t, "log", buf.String())
	if !strings.Contains(buf.String(), "127.0.0.1:1") {
		t.Errorf("warning lost the host: %s", buf.String())
	}
}

// The redacted error still answers errors.Is for a timeout, so callers that
// tell a slow panel from a broken one keep working.
func TestRedactURLError_KeepsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	_, err := client.Get(srv.URL + secretPath)
	if err == nil {
		t.Fatal("request did not time out")
	}
	err = RedactURLError(err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, DeadlineExceeded) = false for %v", err)
	}
	assertNoToken(t, "error", err.Error())
}

// An encrypted happ link decrypts to the subscription URL with the keys shipped
// in this repository, so its payload is as secret as the token itself.
func TestDecryptHappLink_UnknownFormatHidesPayload(t *testing.T) {
	_, err := DecryptHappLink("happ://crypt9/SECRETTOKEN")
	if err == nil {
		t.Fatal("unknown happ format accepted")
	}
	assertNoToken(t, "error", err.Error())
	if !strings.Contains(err.Error(), "crypt9") {
		t.Errorf("error %q no longer says which format it was", err)
	}
}
