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
				fmt.Printf("  🌐 Проверка подключения... ✅ (HTTP %d)\n", resp.StatusCode)
				slog.Info("connectivity check ok", "status", resp.StatusCode)
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		fmt.Printf("  🌐 Проверка подключения не удалась (попытка %d/3), повтор через %v...\n", attempt+1, delay)
		slog.Warn("connectivity check failed", "attempt", attempt+1, "retry_in", delay)
		// P-1: honour Ctrl+C during the backoff instead of sleeping it out.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	fmt.Println("  🌐 Проверка подключения... ❌ не удалась")
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
	fmt.Printf("  ⏳ Ожидание %s порта... ", label)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Printf("✅ %s доступен\n", label)
			slog.Info("port available", "label", label, "port", port)
			return true
		}
		// P-1: abort promptly on Ctrl+C instead of sleeping out the poll interval.
		select {
		case <-ctx.Done():
			fmt.Println("❌ отменено")
			slog.Warn("port wait cancelled", "label", label, "port", port)
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	fmt.Printf("❌ %s не отвечает за %v\n", label, timeout)
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
				fmt.Printf("  🌐 Тестовый запрос %s... ✅ (HTTP %d)\n", proxyURL, resp.StatusCode)
				slog.Info("proxy test ok", "proxy", proxyURL, "status", resp.StatusCode)
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		fmt.Printf("  🌐 Тестовый запрос не удался (попытка %d/3), повтор через %v...\n", attempt+1, delay)
		slog.Warn("proxy test failed", "attempt", attempt+1, "retry_in", delay)
		// P-1: honour Ctrl+C during the backoff instead of sleeping it out.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	fmt.Println("  🌐 Тестовый запрос... ❌ не удался после 3 попыток")
	slog.Error("proxy test failed after 3 attempts")
}

func (a *App) healthCheckLoopPorts(ctx context.Context, socksPort, httpPort int) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	consecutiveFails := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		socksOK := checkPort(ctx, socksPort)
		httpOK := checkPort(ctx, httpPort)

		if socksOK && httpOK {
			consecutiveFails = 0
			continue
		}

		consecutiveFails++
		slog.Warn("health check failed", "socks", socksOK, "http", httpOK,
			"consecutive_fails", consecutiveFails)

		if consecutiveFails >= 3 {
			slog.Warn("3 consecutive health check failures, requesting xray restart")
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.google.com/generate_204", nil)
		resp, err := client.Do(req)
		if err != nil {
			consecutiveFails++
			slog.Warn("TUN connectivity check failed", "consecutive_fails", consecutiveFails)
			if consecutiveFails >= 3 {
				slog.Warn("3 consecutive TUN failures, requesting xray restart")
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

func checkPort(ctx context.Context, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func verifySystemProxy(httpPort int) {
	s := system.ReadProxyState()
	if !s.Enabled {
		fmt.Println("  ⚠️ Системный прокси НЕ включён (реестр не подтвердил)")
		slog.Warn("system proxy not confirmed in registry")
		return
	}
	expected := fmt.Sprintf("127.0.0.1:%d", httpPort)
	if s.Server != expected {
		fmt.Printf("  ⚠️ Системный прокси: %s (ожидалось %s)\n", s.Server, expected)
		slog.Warn("system proxy mismatch", "got", s.Server, "expected", expected)
		return
	}
	fmt.Printf("  ✅ Системный прокси 127.0.0.1:%d подтверждён\n", httpPort)
	slog.Info("system proxy confirmed", "port", httpPort)
}
