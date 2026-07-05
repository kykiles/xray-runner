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
	"xray-runner/internal/subscription"
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

func (a *App) resolveOutbound(ctx context.Context) (json.RawMessage, error) {
	if a.cfg.SubscriptionURL != "" {
		fmt.Println("📡 Загрузка подписки...")
		entries, err := subscription.Fetch(a.cfg.SubscriptionURL)
		if err != nil {
			return nil, fmt.Errorf("subscription: %w", err)
		}
		fmt.Printf("  ✅ Загружено серверов: %d\n", len(entries))

		selected := subscription.ShowMenu(entries)
		printSubEntryDetails(selected)
		resolveServer(selected.Address)

		return subscription.BuildOutboundJSON(selected)
	}

	u, err := url.Parse(a.cfg.VlessURL)
	if err != nil {
		return nil, fmt.Errorf("parse VLESS_URL: %w", err)
	}
	printURLDetails(u)
	resolveServer(hostFromURL(u))

	switch u.Scheme {
	case "vless":
		ob := xraycfg.BuildVLESSOutbound(u)
		return json.Marshal(ob)
	case "ss":
		ob := xraycfg.BuildSSOutbound(u)
		return json.Marshal(ob)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (vless, ss)", u.Scheme)
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

	proxyOutbound, err := a.resolveOutbound(ctx)
	if err != nil {
		return err
	}

	tc, err := xraycfg.LoadTemplate("template.json")
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}

	if a.cfg.Mode == "tun" {
		tc.Inbounds = []xraycfg.Inbound{xraycfg.BuildTUNInbound()}
		fmt.Println("  Режим: TUN (весь трафик через VPN)")
	} else {
		fmt.Println("  Режим: Прокси (HTTP/SOCKS5)")
	}

	cfg := xraycfg.MergeConfig(tc, proxyOutbound)
	cfg.Log = &xraycfg.LogConfig{Loglevel: a.cfg.XrayLogLvl}

	if err := a.writeConfig(cfg); err != nil {
		return err
	}

	printConfigSummary(cfg, a.cfg.Mode)
	fmt.Println("── Routing ──────────────────────────────")
	printRoutingRules(cfg)

	binary := xray.FindBinary()
	slog.Info("starting xray", "binary", binary, "mode", a.cfg.Mode)

	a.runner = xray.New(binary, a.tmpFile)

	xrayCtx, xrayCancel := context.WithCancel(ctx)
	defer xrayCancel()

	go func() {
		if err := a.runner.RunWithRetry(xrayCtx, 5); err != nil {
			slog.Error("xray runner failed", "error", err)
			xrayCancel()
		}
	}()

	if a.cfg.Mode == "tun" {
		err = a.runTun(ctx, cfg)
	} else {
		err = a.runProxy(ctx, cfg)
	}

	// Cancel xray context BEFORE cleanup, so exec.CommandContext kills the
	// process and RunWithRetry returns cleanly (context.Canceled) instead
	// of retrying after a manual Stop()
	xrayCancel()
	time.Sleep(200 * time.Millisecond)
	return err
}

func (a *App) runProxy(ctx context.Context, cfg *xraycfg.XrayConfig) error {
	socksPort, httpPort := a.checkPorts(cfg)
	fmt.Printf("  Порты: SOCKS5 127.0.0.1:%d  HTTP 127.0.0.1:%d\n", socksPort, httpPort)

	if !awaitPort(ctx, socksPort, "SOCKS5", 10*time.Second) {
		return fmt.Errorf("SOCKS5 порт не открылся за 10с")
	}
	if !awaitPort(ctx, httpPort, "HTTP", 5*time.Second) {
		return fmt.Errorf("HTTP порт не открылся за 5с")
	}

	testProxyConnection(ctx, httpPort)

	if err := a.proxy.Enable(httpPort); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
	} else {
		fmt.Println("✅ Системный прокси включён (127.0.0.1:" + strconv.Itoa(httpPort) + ")")
		verifySystemProxy(httpPort)
	}

	fmt.Println("──────────────────────────────────────────")
	go a.healthCheckLoopPorts(ctx, socksPort, httpPort)

	<-ctx.Done()
	fmt.Println("\n🛑 Останавливаем Xray...")
	return nil
}

func (a *App) runTun(ctx context.Context, cfg *xraycfg.XrayConfig) error {
	time.Sleep(2 * time.Second)
	fmt.Print("  ⏳ Ожидание TUN интерфейса... ")
	time.Sleep(3 * time.Second)
	fmt.Println("✅ TUN активирован")

	if a.cfg.KillSwitch {
		fmt.Print("  ⛔ Включение Kill Switch... ")
		if err := system.EnableKillSwitch(); err != nil {
			fmt.Printf("❌ %v\n", err)
		} else {
			fmt.Println("✅")
		}
	}

	testConnectivity(ctx)
	fmt.Println("──────────────────────────────────────────")
	go a.healthCheckLoopConnectivity(ctx)

	<-ctx.Done()
	fmt.Println("\n🛑 Останавливаем Xray...")
	return nil
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
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		fmt.Printf("  🌐 Проверка подключения не удалась (попытка %d/3), повтор через %v...\n", attempt+1, delay)
		time.Sleep(delay)
	}
	fmt.Println("  🌐 Проверка подключения... ❌ не удалась")
}

func (a *App) cleanup() {
	if a.cfg != nil && a.cfg.Mode == "tun" {
		system.DisableKillSwitch()
	} else {
		oldProxy := system.ReadProxyState()
		if oldProxy.Enabled {
			if err := a.proxy.Restore(oldProxy); err != nil {
				slog.Warn("failed to restore proxy", "error", err)
			}
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

func printSubEntryDetails(e *subscription.SubEntry) {
	fmt.Println("── Выбранный сервер ─────────────────────")
	fmt.Printf("  Протокол:  %s\n", e.Protocol)
	fmt.Printf("  Сервер:    %s\n", e.Address)
	fmt.Printf("  Порт:      %d\n", e.Port)
	if e.UUID != "" {
		fmt.Printf("  UUID:      %s\n", maskIfNeeded(e.UUID))
	}
	fmt.Printf("  Transport: %s\n", e.Network)
	if e.Security != "" {
		fmt.Printf("  Security:  %s\n", e.Security)
	}
	if e.Remarks != "" {
		fmt.Printf("  Заметка:   %s\n", e.Remarks)
	}
	fmt.Println("─────────────────────────────────────────")
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

func printConfigSummary(cfg *xraycfg.XrayConfig, mode string) {
	if len(cfg.Outbounds) == 0 {
		return
	}
	var proto struct {
		Protocol string          `json:"protocol"`
		Settings json.RawMessage `json:"settings"`
		Stream   *xraycfg.StreamSettings `json:"streamSettings,omitempty"`
	}
	if err := json.Unmarshal(cfg.Outbounds[0], &proto); err != nil {
		return
	}
	fmt.Printf("📡 Outbound: %s", proto.Protocol)
	if proto.Stream != nil {
		if proto.Stream.Network != "" {
			fmt.Printf(" | transport: %s", proto.Stream.Network)
		}
		if proto.Stream.Security != "" {
			fmt.Printf(" | security: %s", proto.Stream.Security)
		}
	}
	if proto.Settings != nil {
		var addr struct {
			VNext []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"vnext,omitempty"`
			Servers []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"servers,omitempty"`
		}
		if err := json.Unmarshal(proto.Settings, &addr); err == nil {
			if len(addr.VNext) > 0 {
				fmt.Printf(" | server: %s:%d", addr.VNext[0].Address, addr.VNext[0].Port)
			} else if len(addr.Servers) > 0 {
				fmt.Printf(" | server: %s:%d", addr.Servers[0].Address, addr.Servers[0].Port)
			}
		}
	}
	fmt.Printf(" | mode: %s\n", mode)
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

func awaitPort(ctx context.Context, port int, label string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	fmt.Printf("  ⏳ Ожидание %s порта... ", label)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			fmt.Println("❌ отменено")
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Printf("✅ %s доступен\n", label)
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("❌ %s не отвечает за %v\n", label, timeout)
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
				return
			}
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		fmt.Printf("  🌐 Тестовый запрос не удался (попытка %d/3), повтор через %v...\n", attempt+1, delay)
		time.Sleep(delay)
	}
	fmt.Println("  🌐 Тестовый запрос... ❌ не удался после 3 попыток")
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
				a.runner.Stop()
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
					a.runner.Stop()
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
		return
	}
	expected := fmt.Sprintf("127.0.0.1:%d", httpPort)
	if s.Server != expected {
		fmt.Printf("  ⚠️ Системный прокси: %s (ожидалось %s)\n", s.Server, expected)
		return
	}
	fmt.Printf("  ✅ Системный прокси 127.0.0.1:%d подтверждён\n", httpPort)
}
