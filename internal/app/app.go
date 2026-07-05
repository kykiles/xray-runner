package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

type App struct {
	cfg     *config.Config
	runner  *xray.Runner
	proxy   *system.ProxyManager
	tmpFile string
}

func New(cfg *config.Config) *App {
	return &App{
		cfg:    cfg,
		proxy:  system.New(),
		tmpFile: filepath.Join(".", "xray_config.json"),
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

	u, err := url.Parse(a.cfg.VlessURL)
	if err != nil {
		return fmt.Errorf("parse VLESS_URL: %w", err)
	}
	printURLDetails(u)
	resolveServer(hostFromURL(u))

	var proxyOutbound json.RawMessage
	switch u.Scheme {
	case "vless":
		ob := xraycfg.BuildVLESSOutbound(u)
		proxyOutbound, err = json.Marshal(ob)
	case "ss":
		ob := xraycfg.BuildSSOutbound(u)
		proxyOutbound, err = json.Marshal(ob)
	default:
		return fmt.Errorf("unsupported protocol: %s (vless, ss)", u.Scheme)
	}
	if err != nil {
		return fmt.Errorf("marshal outbound: %w", err)
	}

	tc, err := xraycfg.LoadTemplate("template.json")
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}

	cfg := xraycfg.MergeConfig(tc, proxyOutbound)
	cfg.Log = &xraycfg.LogConfig{Loglevel: a.cfg.XrayLogLvl}

	if err := a.writeConfig(cfg); err != nil {
		return err
	}

	printConfigSummary(cfg)
	fmt.Println("── Routing ──────────────────────────────")
	printRoutingRules(cfg)

	binary := xray.FindBinary()
	slog.Info("starting xray", "binary", binary)

	a.runner = xray.New(binary, a.tmpFile)
	if err := a.runner.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("🟢 Xray запущен с PID: %d\n", a.runner.PID())

	socksPort, httpPort := a.checkPorts(cfg)
	fmt.Printf("  Порты: SOCKS5 127.0.0.1:%d  HTTP 127.0.0.1:%d\n", socksPort, httpPort)

	go func() {
		if err := a.runner.Wait(); err != nil {
			slog.Warn("xray exited unexpectedly", "error", err)
		}
	}()

	fmt.Print("  ⏳ Ожидание портов Xray... ")
	if waitForPort(socksPort, 10*time.Second) {
		fmt.Println("✅ SOCKS5 доступен")
	} else {
		fmt.Println("❌ SOCKS5 не отвечает за 10с")
	}
	if waitForPort(httpPort, 5*time.Second) {
		fmt.Println("  ✅ HTTP-прокси доступен")
	} else {
		fmt.Println("  ❌ HTTP-прокси не отвечает")
	}

	testProxyConnection(httpPort)

	if err := a.proxy.Enable(httpPort); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
	} else {
		fmt.Println("✅ Системный прокси включён (127.0.0.1:" + strconv.Itoa(httpPort) + ")")
		verifySystemProxy(httpPort)
	}

	fmt.Println("──────────────────────────────────────────")
	<-ctx.Done()
	fmt.Println("\n🛑 Останавливаем Xray...")

	return nil
}

func (a *App) cleanup() {
	oldProxy := system.ReadProxyState()
	if oldProxy.Enabled {
		if err := a.proxy.Restore(oldProxy); err != nil {
			slog.Warn("failed to restore proxy", "error", err)
		}
	}
	if a.runner != nil {
		_ = a.runner.Stop()
	}
	os.Remove(a.tmpFile)
}

func (a *App) writeConfig(cfg *xraycfg.XrayConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(a.tmpFile, data, 0644)
}

func (a *App) checkPorts(cfg *xraycfg.XrayConfig) (socksPort, httpPort int) {
	socksPort = 10808
	httpPort = 10809
	if len(cfg.Inbounds) > 0 {
		socksPort = cfg.Inbounds[0].Port
	}
	if len(cfg.Inbounds) > 1 {
		httpPort = cfg.Inbounds[1].Port
	}
	return
}

func hostFromURL(u *url.URL) string {
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return u.Host
	}
	return host
}

func resolveServer(host string) {
	fmt.Print("🔍 DNS-резолв сервера... ")
	addrs, err := net.LookupHost(host)
	if err != nil {
		fmt.Printf("❌ ОШИБКА: %v\n", err)
		return
	}
	if len(addrs) == 0 {
		fmt.Println("❌ Нет записей A/AAAA")
		return
	}
	fmt.Printf("✅ %s → %s\n", host, strings.Join(addrs, ", "))
}

func printURLDetails(u *url.URL) {
	fmt.Println("── URL ──────────────────────────────────")
	fmt.Printf("  Протокол:  %s\n", u.Scheme)
	host, port, _ := net.SplitHostPort(u.Host)
	fmt.Printf("  Сервер:    %s\n", host)
	fmt.Printf("  Порт:      %s\n", port)
	if u.Scheme == "vless" {
		q := u.Query()
		fmt.Printf("  UUID:      %s\n", maskIfNeeded(u.User.Username()))
		printParam(q, "type", "Transport")
		printParam(q, "security", "Security")
		printParam(q, "sni", "SNI")
		printParam(q, "fp", "Fingerprint")
		if v := q.Get("pbk"); v != "" {
			fmt.Printf("  PublicKey: %s\n", maskIfNeeded(v))
		}
		if v := q.Get("sid"); v != "" {
			fmt.Printf("  ShortID:   %s\n", maskIfNeeded(v))
		}
		printParam(q, "flow", "Flow")
		printParam(q, "host", "Host")
		printParam(q, "path", "Path")
		printParam(q, "alpn", "ALPN")
	}
	fmt.Println("─────────────────────────────────────────")
}

func printParam(q url.Values, key, label string) {
	if v := q.Get(key); v != "" {
		fmt.Printf("  %-10s %s\n", label+":", v)
	}
}

func maskIfNeeded(s string) string {
	if s == "" {
		return s
	}
	if len(s) <= 8 {
		return s[:2] + "..." + s[len(s)-2:]
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func printConfigSummary(cfg *xraycfg.XrayConfig) {
	if len(cfg.Outbounds) == 0 {
		return
	}
	var ob xraycfg.VLESSOutbound
	if err := json.Unmarshal(cfg.Outbounds[0], &ob); err != nil {
		return
	}
	fmt.Printf("📡 Outbound: %s", ob.Protocol)
	if ob.Stream != nil {
		if ob.Stream.Network != "" {
			fmt.Printf(" | transport: %s", ob.Stream.Network)
		}
		if ob.Stream.Security != "" {
			fmt.Printf(" | security: %s", ob.Stream.Security)
		}
	}
	if ob.Settings != nil && len(ob.Settings.VNext) > 0 {
		vs := ob.Settings.VNext[0]
		fmt.Printf(" | server: %s:%d", vs.Address, vs.Port)
	}
	fmt.Println()
}

func printRoutingRules(cfg *xraycfg.XrayConfig) {
	var routing map[string]interface{}
	if cfg.Routing != nil {
		json.Unmarshal(cfg.Routing, &routing)
	}
	if routing == nil {
		return
	}
	rules, _ := routing["rules"].([]interface{})
	fmt.Printf("  Маршрутов: %d\n", len(rules))
	for i, r := range rules {
		if rule, ok := r.(map[string]interface{}); ok {
			tag, _ := rule["outboundTag"].(string)
			domains, _ := rule["domain"].([]interface{})
			ips, _ := rule["ip"].([]interface{})
			network, _ := rule["network"].(string)
			var parts []string
			if len(domains) > 0 {
				parts = append(parts, fmt.Sprintf("%d доменов", len(domains)))
			}
			if len(ips) > 0 {
				parts = append(parts, fmt.Sprintf("%d IP-сетей", len(ips)))
			}
			if network != "" {
				parts = append(parts, network)
			}
			desc := strings.Join(parts, ", ")
			if desc == "" {
				desc = "catch-all"
			}
			fmt.Printf("    %d. → %-7s  %s\n", i+1, tag, desc)
		}
	}
}

func waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func testProxyConnection(httpPort int) {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	fmt.Printf("  🌐 Тестовый запрос через HTTP-прокси %s... ", proxyURL)

	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	resp, err := client.Get("https://www.google.com/generate_204")
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == 204 || resp.StatusCode == 200 {
		fmt.Printf("✅ (HTTP %d)\n", resp.StatusCode)
	} else {
		fmt.Printf("⚠️  (HTTP %d)\n", resp.StatusCode)
	}
}

func verifySystemProxy(httpPort int) {
	s := system.ReadProxyState()
	if !s.Enabled {
		fmt.Println("  ⚠️ Системный прокси НЕ включён (реестр не подтвердил)")
		return
	}
	expected := fmt.Sprintf("127.0.0.1:%d", httpPort)
	if s.Server != expected {
		fmt.Printf("  ⚠️ Системный прокси: %s (ожидалось %s)\n", s.Server, expected)
		return
	}
	fmt.Printf("  ✅ Системный прокси 127.0.0.1:%d подтверждён\n", httpPort)
}
