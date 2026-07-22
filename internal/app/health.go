package app

// Port/connectivity probes and health-check loops, extracted from app.go (A-1).

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"xray-runner/internal/system"
	"xray-runner/internal/tui"
)

func (a *App) awaitTUNInterface(ctx context.Context, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ifaces, err := a.interfaces()
		if err == nil {
			for _, iface := range ifaces {
				if iface.Name == name && iface.Flags&net.FlagUp != 0 {
					return true
				}
			}
		}
		// P-1: abort promptly on Ctrl+C instead of sleeping out the poll interval.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// reachCheck walks the check URLs until one answers, retrying the whole list
// three times with exponential backoff. The TUN and proxy paths differ only in
// the client they hand in: one goes out directly, the other through xray's http
// inbound. label names the check in the log.
func reachCheck(ctx context.Context, client *http.Client, urls []string, label string, logArgs ...any) {
	for attempt := 0; attempt < 3; attempt++ {
		for _, testURL := range urls {
			select {
			case <-ctx.Done():
				return
			default:
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", testURL, nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode == 204 || resp.StatusCode == 200 {
				slog.Info(label+" ok", append(logArgs, "status", resp.StatusCode)...)
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		slog.Warn(label+" failed", "attempt", attempt+1, "retry_in", delay)
		// P-1: honour Ctrl+C during the backoff instead of sleeping it out.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	slog.Error(label + " failed after 3 attempts")
}

// dialPort reports whether something is listening on the local port right now.
// Every port probe in the package — "is it taken", "is it up yet", "is it still
// alive" — is this one dial with a different timeout.
func dialPort(ctx context.Context, port int, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// portInUse reports whether something is already listening on the local port,
// which before xray starts means another process (likely a second instance).
func portInUse(port int) bool {
	return dialPort(context.Background(), port, 300*time.Millisecond)
}

// awaitPort polls until the port accepts connections or the timeout runs out.
// An empty label keeps the wait silent (the benchmark measures dozens of ports
// and has its own reporting).
func awaitPort(ctx context.Context, port int, label string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if dialPort(ctx, port, 500*time.Millisecond) {
			if label != "" {
				slog.Info("port available", "label", label, "port", port)
			}
			return true
		}
		// P-1: abort promptly on Ctrl+C instead of sleeping out the poll interval.
		select {
		case <-ctx.Done():
			if label != "" {
				slog.Warn("port wait cancelled", "label", label, "port", port)
			}
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	if label != "" {
		slog.Error("port timeout", "label", label, "port", port, "timeout", timeout)
	}
	return false
}

func (a *App) testProxyConnection(ctx context.Context, httpPort int) {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}
	defer transport.CloseIdleConnections()

	reachCheck(ctx, &http.Client{Transport: transport, Timeout: 10 * time.Second},
		a.cfg.CheckURLs(), "proxy test", "proxy", proxyURL)
}

func (a *App) healthCheckLoopPorts(ctx context.Context, socksPort, httpPort int) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	consecutiveFails := 0
	// Probe once up front so the status screen shows a real result immediately
	// instead of "проверка…" for the first interval.
	first := true

	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
		first = false

		socksOK := dialPort(ctx, socksPort, 2*time.Second)
		httpOK := dialPort(ctx, httpPort, 2*time.Second)
		// No latency: this probe dials our own local ports, so its timing says
		// nothing about the VPN and would read as a fake "0 ms" to the server.
		a.recordHealth(socksOK && httpOK, 0)

		if socksOK && httpOK {
			consecutiveFails = 0
			continue
		}

		consecutiveFails++
		slog.Warn("health check failed", "socks", socksOK, "http", httpOK,
			"consecutive_fails", consecutiveFails)

		if consecutiveFails >= 3 {
			a.announceRestart()
			if a.runner != nil {
				a.runner.RequestRestart()
			}
			consecutiveFails = 0
		}
	}
}

func (a *App) healthCheckLoopConnectivity(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	consecutiveFails := 0
	client := &http.Client{Timeout: 10 * time.Second}
	checkURL := a.cfg.CheckURLs()[0]
	probe := func() (bool, time.Duration) {
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return false, time.Since(start)
		}
		_ = resp.Body.Close()
		return true, time.Since(start)
	}

	// Probe once up front so the status screen shows a real result immediately.
	first := true

	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}

		var ok bool
		var latency time.Duration
		if first {
			// A freshly-established TUN route needs a moment before it carries
			// traffic; give the first check a warm-up window of retries so a
			// cold miss doesn't flash "нет связи" the instant we connect.
			ok, latency = connectivityWarmup(ctx, probe, connectivityWarmupWindow, connectivityWarmupInterval)
		} else {
			ok, latency = probe()
		}
		first = false

		a.recordHealth(ok, latency)
		if !ok {
			consecutiveFails++
			slog.Warn("TUN connectivity check failed", "consecutive_fails", consecutiveFails)
			if consecutiveFails >= 3 {
				a.announceRestart()
				if a.runner != nil {
					a.runner.RequestRestart()
				}
				consecutiveFails = 0
			}
			continue
		}
		consecutiveFails = 0
	}
}

const (
	// connectivityWarmupWindow / Interval bound the first-probe warm-up: retry
	// for up to the window, waiting the interval between tries.
	connectivityWarmupWindow   = 12 * time.Second
	connectivityWarmupInterval = 2 * time.Second
)

// connectivityWarmup runs probe until it succeeds or the window elapses,
// returning the last result. It exists so the very first health check can ride
// out a cold TUN route instead of reporting a transient failure as "нет связи".
func connectivityWarmup(ctx context.Context, probe func() (bool, time.Duration), window, interval time.Duration) (bool, time.Duration) {
	deadline := time.Now().Add(window)
	for {
		ok, latency := probe()
		if ok {
			return true, latency
		}
		if !time.Now().Before(deadline) {
			return false, latency
		}
		select {
		case <-ctx.Done():
			return false, latency
		case <-time.After(interval):
		}
	}
}

// recordHealth stores the latest probe result and pushes it to the connected
// screen (U-5).
func (a *App) recordHealth(ok bool, latency time.Duration) {
	a.statusMu.Lock()
	a.lastCheck = time.Now()
	a.lastCheckOK = ok
	a.statusMu.Unlock()

	u := tui.StatusUpdate{OK: ok}
	if ok {
		u.Latency = latency
	}
	a.publishStatus(u)
}

// publishStatus hands an update to the status screen. The screen is optional
// (scripted runs have none) and must never block a health loop, so a full or
// absent channel simply drops the update.
func (a *App) publishStatus(u tui.StatusUpdate) {
	a.statusMu.Lock()
	ch := a.statusCh
	a.statusMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- u:
	default:
	}
}

// announceRestart tells the user (not just the log) that xray is being
// restarted after failed health checks (U-5).
func (a *App) announceRestart() {
	slog.Warn("3 consecutive health check failures, requesting xray restart")
	a.publishStatus(tui.StatusUpdate{Note: "🔁 Health-check не прошёл 3 раза подряд — перезапускаю xray…"})
}

// verifySystemProxy reports whether the OS actually took the proxy setting; a
// mismatch surfaces on the status screen, since it means traffic is not going
// through xray.
func (a *App) verifySystemProxy(httpPort int) {
	s := system.ReadProxyState()
	if !s.Enabled {
		slog.Warn("system proxy not confirmed")
		a.publishStatus(tui.StatusUpdate{Note: "⚠ Системный прокси не подтверждён системой", Err: true})
		return
	}
	expected := fmt.Sprintf("127.0.0.1:%d", httpPort)
	if s.Server != expected {
		slog.Warn("system proxy mismatch", "got", s.Server, "expected", expected)
		a.publishStatus(tui.StatusUpdate{
			Note: fmt.Sprintf("⚠ Системный прокси: %s (ожидался %s)", s.Server, expected),
			Err:  true,
		})
		return
	}
	slog.Info("system proxy confirmed", "port", httpPort)
}
