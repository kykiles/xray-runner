package subscription

// A05: the device headers were dropped on a hop to a new host and put back on
// the next one — Go copies the first request's headers onto every redirect —
// so panel → third/a → third/b handed the device id to the third party. Go also
// sends the previous URL, subscription token and all, as the Referer. Plain
// http was judged only on the first URL.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type hop struct {
	url    string
	header http.Header
}

// chainTransport answers from a script instead of the network: a URL listed in
// redirects answers 302 to its target, anything else answers 200. Every request
// that reaches it is recorded, so a refused hop shows up as a missing one.
type chainTransport struct {
	redirects map[string]string
	hops      []hop
}

func (c *chainTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.hops = append(c.hops, hop{url: r.URL.String(), header: r.Header.Clone()})
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    r,
	}
	if loc, ok := c.redirects[r.URL.String()]; ok {
		resp.StatusCode = http.StatusFound
		resp.Header.Set("Location", loc)
	}
	return resp, nil
}

func withChain(t *testing.T, redirects map[string]string) *chainTransport {
	t.Helper()
	c := &chainTransport{redirects: redirects}
	orig := fetchTransport
	fetchTransport = c
	t.Cleanup(func() { fetchTransport = orig })
	return c
}

func fetchWithDevice(start string) error {
	_, _, err := fetchBody(context.Background(), start, WithHWID("dummy-hwid", "linux", "dummy-model"))
	return err
}

const panelSub = "https://panel.invalid" + secretPath

func TestFetchBody_DeviceHeadersOnlyToOriginalOrigin(t *testing.T) {
	cases := []struct {
		name      string
		start     string
		redirects map[string]string
		// origin says, per request in order, whether it goes to the origin of
		// the first one — the only place the device headers may go.
		origin []bool
	}{
		{"third party twice", panelSub, map[string]string{
			panelSub:                  "https://third.invalid/a",
			"https://third.invalid/a": "https://third.invalid/b",
		}, []bool{true, false, false}},
		{"third then fourth", panelSub, map[string]string{
			panelSub:                  "https://third.invalid/a",
			"https://third.invalid/a": "https://fourth.invalid/b",
		}, []bool{true, false, false}},
		{"same origin chain", panelSub, map[string]string{
			panelSub:                  "https://panel.invalid/a",
			"https://panel.invalid/a": "https://panel.invalid/b",
		}, []bool{true, true, true}},
		{"back to the origin", panelSub, map[string]string{
			panelSub:                  "https://third.invalid/a",
			"https://third.invalid/a": "https://panel.invalid/final",
		}, []bool{true, false, true}},
		{"other port", panelSub, map[string]string{
			panelSub: "https://panel.invalid:8443/final",
		}, []bool{true, false}},
		{"explicit default port", panelSub, map[string]string{
			panelSub: "https://panel.invalid:443/final",
		}, []bool{true, true}},
		{"host case", panelSub, map[string]string{
			panelSub: "https://PANEL.invalid/final",
		}, []bool{true, true}},
		{"subdomain", panelSub, map[string]string{
			panelSub: "https://cdn.panel.invalid/final",
		}, []bool{true, false}},
		{"loopback http to https", "http://127.0.0.1:8080" + secretPath, map[string]string{
			"http://127.0.0.1:8080" + secretPath: "https://panel.invalid/final",
		}, []bool{true, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := withChain(t, c.redirects)
			if err := fetchWithDevice(c.start); err != nil {
				t.Fatalf("fetchBody: %v", err)
			}
			if len(chain.hops) != len(c.origin) {
				t.Fatalf("%d requests went out, want %d", len(chain.hops), len(c.origin))
			}
			for i, h := range chain.hops {
				for _, name := range []string{"X-Hwid", "X-Device-Os", "X-Device-Model"} {
					if has := h.header.Get(name) != ""; has != c.origin[i] {
						t.Errorf("request %d (%s): %s present = %v, want %v", i, h.url, name, has, c.origin[i])
					}
				}
				// The Referer is the previous URL, token included.
				if !c.origin[i] && h.header.Get("Referer") != "" {
					t.Errorf("request %d (%s): Referer %q sent off the origin", i, h.url, h.header.Get("Referer"))
				}
			}
		})
	}
}

// A redirect target is held to the rule the first URL is: https anywhere, plain
// http only to loopback — and never after https. The refused request must not
// go out, and the refusal must not print the token.
func TestFetchBody_RedirectTransportPolicy(t *testing.T) {
	cases := []struct {
		name      string
		start     string
		redirects map[string]string
		requests  int
	}{
		{"https to http", panelSub, map[string]string{
			panelSub: "http://panel.invalid" + secretPath,
		}, 1},
		{"https to loopback http", panelSub, map[string]string{
			panelSub: "http://127.0.0.1:8080" + secretPath,
		}, 1},
		{"loopback http to external http", "http://127.0.0.1:8080" + secretPath, map[string]string{
			"http://127.0.0.1:8080" + secretPath: "http://external.invalid" + secretPath,
		}, 1},
		{"https to another scheme", panelSub, map[string]string{
			panelSub: "ftp://panel.invalid" + secretPath,
		}, 1},
		{"redirect loop", panelSub, map[string]string{
			panelSub: panelSub,
		}, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := withChain(t, c.redirects)
			err := fetchWithDevice(c.start)
			if err == nil {
				t.Fatal("fetchBody followed the redirect")
			}
			if len(chain.hops) != c.requests {
				t.Errorf("%d requests went out, want %d", len(chain.hops), c.requests)
			}
			assertNoToken(t, "refusal", err.Error())
		})
	}
}

// The first URL gets the same rule: anything but https and loopback http is
// refused before a request goes out.
func TestFetchBody_RefusesOtherSchemes(t *testing.T) {
	chain := withChain(t, nil)
	err := fetchWithDevice("ftp://panel.invalid" + secretPath)
	if err == nil {
		t.Fatal("ftp:// subscription accepted")
	}
	if len(chain.hops) != 0 {
		t.Errorf("%d request(s) went out before the refusal", len(chain.hops))
	}
	assertNoToken(t, "refusal", err.Error())
}
