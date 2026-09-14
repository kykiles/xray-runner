package app

// A06: proxy mode's health check dialled its own two local ports and called the
// session healthy when both answered — whether or not anything came back from
// the server. It now asks for a check URL through the session's http inbound,
// the way the tun check goes through its probe inbound, and only an answer
// counts.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/tui"
)

// fastHealth shortens the health loop's waits so a test sees several checks
// well within a second: no warm-up retries, a short tick, a short request
// timeout.
func fastHealth(t *testing.T) {
	t.Helper()
	interval, window, retry, client := proxyCheckInterval, connectivityWarmupWindow, connectivityWarmupInterval, newProbeClient
	proxyCheckInterval, connectivityWarmupWindow, connectivityWarmupInterval = 20*time.Millisecond, 0, 10*time.Millisecond
	newProbeClient = func(port int, _ time.Duration) *http.Client { return client(port, 500*time.Millisecond) }
	t.Cleanup(func() {
		proxyCheckInterval, connectivityWarmupWindow, connectivityWarmupInterval, newProbeClient = interval, window, retry, client
	})
}

// proxyHealth is a proxy session's health loop running against the given ports,
// with everything it sends to the screen kept for the test.
type proxyHealth struct {
	a    *App
	ch   chan tui.StatusUpdate
	stop func()
}

func startProxyHealth(t *testing.T, ports sessionPorts, urls ...string) *proxyHealth {
	t.Helper()
	// PROXY_SYSTEM is off (the zero Config): the check must not need the system
	// proxy setting.
	a := &App{cfg: &config.Config{HealthCheckURLs: urls}}
	h := &proxyHealth{a: a, ch: make(chan tui.StatusUpdate, 64)}
	a.setStatusCh(h.ch)
	h.stop = a.startHealth(context.Background(), ports, false)
	t.Cleanup(h.stop)
	return h
}

// next returns the next health result, skipping notes.
func (h *proxyHealth) next(t *testing.T) tui.StatusUpdate {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-h.ch:
			if u.Note == "" && u.Apps == nil {
				return u
			}
		case <-deadline:
			t.Fatal("no health result")
		}
	}
}

// plainListener accepts connections and hangs up at once: a port that is open
// and is no proxy.
func plainListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// closedPort is a local port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func serverPort(srv *httptest.Server) int {
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// fakeInbound stands in for the core's http inbound: handle gets every request
// the check sends through it, addressed to the check URL's host.
func fakeInbound(t *testing.T, handle http.HandlerFunc) sessionPorts {
	t.Helper()
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	port := serverPort(srv)
	return sessionPorts{socks: port, http: port}
}

// The audit's case: both ports open, nothing behind them. That is not healthy.
func TestProxyHealth_OpenPortsAreNotHealthy(t *testing.T) {
	fastHealth(t)
	h := startProxyHealth(t, sessionPorts{socks: plainListener(t), http: plainListener(t)}, "http://check.invalid/generate_204")
	if u := h.next(t); u.OK {
		t.Fatal("proxy reported healthy with two open ports and no proxy behind them")
	}
}

func TestProxyHealth_InboundFailureIsNotHealthy(t *testing.T) {
	fastHealth(t)
	t.Run("502", func(t *testing.T) {
		ports := fakeInbound(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
		if u := startProxyHealth(t, ports, "http://check.invalid/generate_204").next(t); u.OK {
			t.Error("a 502 from the inbound reported healthy")
		}
	})
	t.Run("no answer", func(t *testing.T) {
		release := make(chan struct{})
		ports := fakeInbound(t, func(http.ResponseWriter, *http.Request) { <-release })
		defer close(release)
		if u := startProxyHealth(t, ports, "http://check.invalid/generate_204").next(t); u.OK {
			t.Error("an inbound that never answers reported healthy")
		}
	})
}

// A working path: the inbound tunnels to a TLS target (CONNECT) and the target
// answers 204.
func TestProxyHealth_AnswerThroughTheInboundIsHealthy(t *testing.T) {
	fastHealth(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	var tunnels atomic.Int32
	ports := fakeInbound(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		tunnels.Add(1)
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(up, conn)
			_ = up.Close()
		}()
		_, _ = io.Copy(conn, up)
		_ = conn.Close()
	})

	// The test target is trusted in the probe's own transport, nowhere else.
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	client := newProbeClient
	newProbeClient = func(port int, timeout time.Duration) *http.Client {
		c := client(port, timeout)
		c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
		return c
	}
	t.Cleanup(func() { newProbeClient = client })

	u := startProxyHealth(t, ports, target.URL+"/generate_204").next(t)
	if !u.OK || u.Latency <= 0 {
		t.Errorf("result = %+v, want healthy, with the latency of the answer", u)
	}
	if tunnels.Load() == 0 {
		t.Error("the check never went through the inbound")
	}
}

// With the inbound down nothing else may answer for it — here the check host
// reached directly.
func TestProxyHealth_OnlyThroughTheInbound(t *testing.T) {
	fastHealth(t)
	var direct atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		direct.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	dead := closedPort(t)
	if u := startProxyHealth(t, sessionPorts{socks: dead, http: dead}, target.URL+"/generate_204").next(t); u.OK {
		t.Error("reported healthy with the inbound down")
	}
	if n := direct.Load(); n != 0 {
		t.Errorf("the check host was reached directly %d times", n)
	}
}

// A blocked first check URL is not a failed check while another one answers.
func TestProxyHealth_FirstURLBlockedSecondAnswers(t *testing.T) {
	fastHealth(t)
	ports := fakeInbound(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "blocked.invalid" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if u := startProxyHealth(t, ports, "http://blocked.invalid/x", "http://check.invalid/generate_204").next(t); !u.OK {
		t.Error("reported down although the second check URL answered")
	}
}

// A 3xx is a failed check, not a lead to follow: the host it names is not under
// the probe's routing rule, so the core may send it out direct, and an answer
// from there says nothing about the tunnel.
func TestProxyHealth_RedirectIsNotHealthy(t *testing.T) {
	fastHealth(t)
	var elsewhere atomic.Int32
	ports := fakeInbound(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "check.invalid" {
			http.Redirect(w, r, "http://elsewhere.invalid/", http.StatusFound)
			return
		}
		elsewhere.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	if u := startProxyHealth(t, ports, "http://check.invalid/generate_204").next(t); u.OK {
		t.Error("a redirect reported healthy")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the check followed the redirect %d times", n)
	}
}

// Three failed checks in a row ask for one restart; a check that passes starts
// the count over.
func TestHealthLoop_ThreeFailedChecksAskOneRestart(t *testing.T) {
	fastHealth(t)
	results := []bool{false, false, false, false, false, true, false, false}
	var mu sync.Mutex
	next := 0
	a := &App{cfg: &config.Config{}}
	a.healthProbe = func() (bool, time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if next == len(results) {
			return true, time.Millisecond
		}
		next++
		return results[next-1], time.Millisecond
	}
	ch := make(chan tui.StatusUpdate, 64)
	a.setStatusCh(ch)
	stop := a.startHealth(context.Background(), sessionPorts{}, false)
	defer stop()

	restarts := 0
	deadline := time.After(5 * time.Second)
	for checks := 0; checks <= len(results); {
		select {
		case u := <-ch:
			switch {
			case strings.Contains(u.Note, "перезапускаю"):
				restarts++
			case u.Note == "" && u.Apps == nil:
				checks++
			}
		case <-deadline:
			t.Fatal("the loop stopped checking")
		}
	}
	if restarts != 1 {
		t.Errorf("restarts = %d, want 1", restarts)
	}
}

// Every check URL failing is one failed check, not three: the restart comes
// after three rounds of the whole list.
func TestProxyHealth_EveryURLFailingIsOneCheck(t *testing.T) {
	fastHealth(t)
	var asked atomic.Int32
	ports := fakeInbound(t, func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})
	h := startProxyHealth(t, ports, "http://a.invalid/", "http://b.invalid/", "http://c.invalid/")

	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-h.ch:
			if strings.Contains(u.Note, "перезапускаю") {
				if n := asked.Load(); n < 9 {
					t.Errorf("restart asked after %d requests, want three rounds of all 3 URLs", n)
				}
				return
			}
		case <-deadline:
			t.Fatal("no restart after three failed checks")
		}
	}
}

// Stopping the loop leaves nothing behind: its keep-alive connection to the
// inbound goes with it.
func TestProxyHealth_StopClosesItsConnections(t *testing.T) {
	fastHealth(t)
	var open atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	srv.Start()
	defer srv.Close()
	port := serverPort(srv)

	h := startProxyHealth(t, sessionPorts{socks: port, http: port}, "http://check.invalid/generate_204")
	if u := h.next(t); !u.OK {
		t.Fatalf("result = %+v, want healthy", u)
	}
	h.stop()
	deadline := time.Now().Add(time.Second)
	for open.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections to the inbound still open after the loop stopped", open.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every session config sends the probe hosts down the path under test, once and
// ahead of the panel's rules — the template in proxy mode too, where the check
// used to follow whatever the template's rules said (A06).
func TestBuildSessionConfig_OneProbeRuleAhead(t *testing.T) {
	single := &subscription.SubEntry{
		Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		RawOutbound: outboundNamed(t, "proxy-2"),
	}
	cases := []struct {
		name     string
		tgt      *target
		aim, tag string
	}{
		{"template", splitTarget(), "outboundTag", "proxy"},
		{"single server of a profile", &target{profileName: "Auto", entry: single, profileRaw: json.RawMessage(panelProfile)}, "outboundTag", "proxy-2"},
		{"profile", &target{profileName: "Auto", profileRaw: json.RawMessage(panelProfile)}, "balancerTag", "Balancer"},
	}
	for _, mode := range []string{"proxy", "tun"} {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				a := newTemplateApp(t)
				a.mode = mode
				raw, _, err := a.buildSessionConfig(c.tgt)
				if err != nil {
					t.Fatalf("buildSessionConfig: %v", err)
				}
				var cfg struct {
					Routing struct {
						Rules []struct {
							Domain      []string `json:"domain"`
							OutboundTag string   `json:"outboundTag"`
							BalancerTag string   `json:"balancerTag"`
						} `json:"rules"`
					} `json:"routing"`
				}
				if err := json.Unmarshal(raw, &cfg); err != nil {
					t.Fatalf("config is not valid JSON: %v", err)
				}
				hosts := probeHosts(a.cfg.CheckURLs())
				count := 0
				for _, r := range cfg.Routing.Rules {
					if slices.Equal(r.Domain, hosts) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("%d probe rules, want exactly 1", count)
				}
				first := cfg.Routing.Rules[0]
				aim := map[string]string{"outboundTag": first.OutboundTag, "balancerTag": first.BalancerTag}[c.aim]
				if !slices.Equal(first.Domain, hosts) || aim != c.tag {
					t.Errorf("first rule = %+v, want the probe rule aimed at %s %q", first, c.aim, c.tag)
				}
			})
		}
	}
}
