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

func testConnectivity(ctx context.Context) {
	testURLs := []string{
		"https://www.google.com/generate_204",
		"https://connectivitycheck.gstatic.com/generate_204",
		"https://www.cloudflare.com/cdn-cgi/trace",
	}

	client := &http.Client{Timeout: 10 * time.Second}

	for attempt := 0; attempt < 3; attempt++ {
		for _, testURL := range testURLs {
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
			resp.Body.Close()
			if resp.StatusCode == 204 || resp.StatusCode == 200 {
				slog.Info("connectivity check ok", "status", resp.StatusCode)
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		slog.Warn("connectivity check failed", "attempt", attempt+1, "retry_in", delay)
		// P-1: honour Ctrl+C during the backoff instead of sleeping it out.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	slog.Error("connectivity check failed after 3 attempts")
}

// portInUse reports whether something is already listening on the local port,
// which before xray starts means another process (likely a second instance).
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func awaitPort(ctx context.Context, port int, label string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			slog.Info("port available", "label", label, "port", port)
			return true
		}
		// P-1: abort promptly on Ctrl+C instead of sleeping out the poll interval.
		select {
		case <-ctx.Done():
			slog.Warn("port wait cancelled", "label", label, "port", port)
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	slog.Error("port timeout", "label", label, "port", port, "timeout", timeout)
	return false
}

func testProxyConnection(ctx context.Context, httpPort int) {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	testURLs := []string{
		"https://www.google.com/generate_204",
		"https://connectivitycheck.gstatic.com/generate_204",
		"https://www.cloudflare.com/cdn-cgi/trace",
	}

	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}

	for attempt := 0; attempt < 3; attempt++ {
		for _, testURL := range testURLs {
			select {
			case <-ctx.Done():
				return
			default:
			}

			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, "GET", testURL, nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == 204 || resp.StatusCode == 200 {
				slog.Info("proxy test ok", "proxy", proxyURL, "status", resp.StatusCode)
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		slog.Warn("proxy test failed", "attempt", attempt+1, "retry_in", delay)
		// P-1: honour Ctrl+C during the backoff instead of sleeping it out.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	slog.Error("proxy test failed after 3 attempts")
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

		socksOK := checkPort(ctx, socksPort)
		httpOK := checkPort(ctx, httpPort)
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
		first = false

		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.google.com/generate_204", nil)
		resp, err := client.Do(req)
		a.recordHealth(err == nil, time.Since(start))
		if err != nil {
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
		resp.Body.Close()
		consecutiveFails = 0
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

func checkPort(ctx context.Context, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
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
