package app

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
)

// A run killed before its teardown leaves the system proxy on its dead local
// port. Only that is flagged: a proxy that answers, one on another host, PAC and
// a proxy that is off are all somebody's working setting (A11).
func TestDeadLoopbackProxy(t *testing.T) {
	up := map[string]bool{"127.0.0.1:7890": true}
	listening := func(addr string) bool { return up[addr] }
	cases := []struct {
		name string
		s    system.ProxyState
		want string
	}{
		{"our port, nothing listening", system.ProxyState{Enabled: true, Server: "127.0.0.1:10809"}, "127.0.0.1:10809"},
		{"localhost", system.ProxyState{Enabled: true, Server: "localhost:10809"}, "localhost:10809"},
		{"ipv6 loopback", system.ProxyState{Enabled: true, Server: "[::1]:10809"}, "[::1]:10809"},
		{"with a scheme", system.ProxyState{Enabled: true, Server: "http://127.0.0.1:10809"}, "127.0.0.1:10809"},
		{"per protocol", system.ProxyState{Enabled: true, Server: "http=127.0.0.1:7890;https=127.0.0.1:10809"}, "127.0.0.1:10809"},
		{"a live local proxy", system.ProxyState{Enabled: true, Server: "127.0.0.1:7890"}, ""},
		{"another host", system.ProxyState{Enabled: true, Server: "proxy.corp.local:3128"}, ""},
		{"pac", system.ProxyState{Mode: "auto", Server: "127.0.0.1:10809"}, ""},
		{"proxy off", system.ProxyState{Server: "127.0.0.1:10809"}, ""},
		{"nothing read", system.ProxyState{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deadLoopbackProxy(c.s, listening); got != c.want {
				t.Errorf("deadLoopbackProxy(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

// The warning is a note and a log line, never a change: the setting stays as
// found, so the session's snapshot is still the exact one, and a run without a
// terminal gets the warning in its log without waiting on anyone (A11).
func TestWarnDeadLoopbackProxy(t *testing.T) {
	for _, headless := range []bool{false, true} {
		t.Run(fmt.Sprintf("headless=%v", headless), func(t *testing.T) {
			a := New(&config.Config{}, Options{NonInteractive: headless})
			a.noTTY = headless
			reads := 0
			a.readProxyState = func() system.ProxyState {
				reads++
				return system.ProxyState{Enabled: true, Server: "127.0.0.1:10809"}
			}
			a.proxyListening = func(string) bool { return false }
			a.restoreProxy = func(system.ProxyState) error {
				t.Error("the proxy setting was written")
				return nil
			}
			a.pendingNote = "Прошлый режим TUN недоступен."

			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			done := make(chan struct{})
			go func() {
				a.warnDeadLoopbackProxy()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("the warning waited on something")
			}

			if reads != 1 {
				t.Errorf("proxy setting read %d times, want once", reads)
			}
			for _, want := range []string{"Прошлый режим TUN недоступен.", "127.0.0.1:10809", "README"} {
				if !strings.Contains(a.pendingNote, want) {
					t.Errorf("note %q lacks %q", a.pendingNote, want)
				}
			}
			if !strings.Contains(logs.String(), "127.0.0.1:10809") {
				t.Errorf("log lacks the address:\n%s", logs.String())
			}
		})
	}
}

// listeningAt tells a port somebody accepts on from one nobody does.
func TestListeningAt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if !listeningAt(addr) {
		t.Errorf("listeningAt(%s) = false with a listener up", addr)
	}
	_ = ln.Close()
	if listeningAt(addr) {
		t.Errorf("listeningAt(%s) = true after the listener closed", addr)
	}
}
