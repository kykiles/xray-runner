package updater

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type scriptedTransport func(*http.Request) (*http.Response, error)

func (f scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectTo answers every request with a 302 to loc, without a socket.
func redirectTo(t *testing.T, loc string) {
	t.Helper()
	testTransport = scriptedTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{loc}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() { testTransport = nil })
}

// A panel's database URL may hold a token, and the error net/http builds prints
// the URL it was given. A Location it cannot parse is worse: the failure is
// raised inside the library, before checkRedirect ever sees the target, and its
// text quotes that target in full. Neither may reach a log line or the screen,
// on any of the three ways the updater reaches the network (F06).
func TestFetchers_RefusedRedirectHidesToken(t *testing.T) {
	for _, loc := range []struct{ name, location string }{
		{"unparseable location", "https://panel.example/" + secretToken + "/%zz?secret=" + secretToken},
		{"downgrade to http", "http://panel.example/" + secretToken},
	} {
		for fname, fetch := range fetchers {
			t.Run(loc.name+"/"+fname, func(t *testing.T) {
				redirectTo(t, loc.location)
				assertHidesToken(t, fetch(t, "https://panel.example/start"))
			})
		}
	}
}

// url.Parse quotes the whole URL it could not read, and that URL is the one
// holding the token.
func TestNewGet_MalformedURLHidesToken(t *testing.T) {
	_, err := newGet(context.Background(), "https://panel.example/"+secretToken+"/%zz")
	assertHidesToken(t, err)
}
