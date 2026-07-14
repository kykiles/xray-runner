package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/xray"
	"xray-runner/internal/ui"
	"xray-runner/internal/xraycfg"
)

type App struct {
	cfg             *config.Config
	runner          *xray.Runner
	proxy           *system.ProxyManager
	tmpFile         string
	lockFile        string
	lockHeld        bool
	binary          string
	serverHost      string
	serverPort      int
	serverUDP       bool
	originalProxy   system.ProxyState
	proxyTouched    bool
	interfaces      func() ([]net.Interface, error)
}

func New(cfg *config.Config) *App {
	return &App{
		cfg:        cfg,
		proxy:      system.New(),
		tmpFile:    filepath.Join(".", "xray_config.json"),
		lockFile:   filepath.Join(".", "xray_config.json.lock"),
		interfaces: net.Interfaces,
	}
}

func (a *App) resolveOutbound(ctx context.Context, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	if len(subs) == 0 && (a.cfg.SubscriptionURL != "" || a.cfg.VlessURL != "") {
		return a.resolveLegacyOutbound(ctx, tc)
	}

	for {
		subURL := a.selectSubscription(subs)
		if subURL == "" {
			if err := a.addSubFlow(); err != nil {
				ui.Error(err.Error())
				subs, _ = subscription.LoadSubscriptions()
				if len(subs) == 0 {
					continue
				}
			} else {
				subs, _ = subscription.LoadSubscriptions()
			}
			continue
		}

		outbound, err := a.fetchAndSelectServer(ctx, subURL, tc)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			if err.Error() != "switch subscription" {
				ui.Error(err.Error())
			}
			continue
		}
		return outbound, nil
	}
}

func (a *App) resolveLegacyOutbound(ctx context.Context, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	if a.cfg.SubscriptionURL != "" {
		for {
			fmt.Println("📡 Загрузка подписки...")
			hwid := config.GetOrCreateHWID(a.cfg.HWID)
			entries, err := subscription.FetchWithHWID(a.cfg.SubscriptionURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
			if err != nil {
				return nil, fmt.Errorf("subscription: %w", err)
			}
			slog.Info("subscription loaded", "servers", len(entries))

			refresh := func() ([]subscription.SubEntry, error) {
				return subscription.FetchWithHWID(a.cfg.SubscriptionURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
			}
			selected := subscription.ShowMenu(ctx, entries, refresh, nil)
			if selected == nil {
				continue
			}
			if err := selected.Validate(); err != nil {
				return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
			}
			selected.AllowInsecure = a.cfg.AllowInsecure
			a.printSubEntryDetails(selected)
			a.setServerEndpoint(selected)
			resolveServer(selected.Address)
			return subscription.BuildOutboundJSON(selected)
		}
	}

	u, err := url.Parse(a.cfg.VlessURL)
	if err != nil {
		return nil, fmt.Errorf("parse VLESS_URL: %w", err)
	}
	a.printURLDetails(u)
	a.serverHost = hostFromURL(u)
	if _, portStr, err := net.SplitHostPort(u.Host); err == nil {
		a.serverPort, _ = strconv.Atoi(portStr)
	}
	resolveServer(a.serverHost)

	switch u.Scheme {
	case "vless":
		ob, err := xraycfg.BuildVLESSOutbound(u)
		if err != nil {
			return nil, fmt.Errorf("build vless: %w", err)
		}
		return json.Marshal(ob)
	case "ss":
		ob, err := xraycfg.BuildSSOutbound(u)
		if err != nil {
			return nil, fmt.Errorf("build ss: %w", err)
		}
		return json.Marshal(ob)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (vless, ss)", u.Scheme)
	}
}

func (a *App) selectSubscription(subs []subscription.NamedSubscription) string {
	for {
		ui.ClearScreen()
		ui.Title("Список моих подписок")
		for i, s := range subs {
			ui.Item(i+1, fmt.Sprintf("%-30s  %s", s.Name, ui.Dim(maskURL(s.URL, a.cfg.MaskCreds))))
		}
		ui.Divider()

		prompt := fmt.Sprintf("[ + - добавить, 0 - выход, 1-%d - выбор подписки, d - удалить ]", len(subs))
		fmt.Printf("  %s▸%s %s ", ui.ColorCyan, ui.ColorReset, prompt)
		input, err := ui.ReadKey()
		fmt.Println()
		if err != nil {
			ui.Error("Ошибка ввода")
			continue
		}

		switch {
		case input == "+":
			return ""
		case strings.EqualFold(input, "d"):
			if len(subs) == 0 {
				ui.Error("Нет подписок для удаления")
				continue
			}
			fmt.Printf("  Введите номер для удаления [1-%d]: ", len(subs))
			delKey, err := ui.ReadKey()
			fmt.Println()
			if err != nil {
				continue
			}
			delIdx, err := strconv.Atoi(delKey)
			if err != nil || delIdx < 1 || delIdx > len(subs) {
				ui.Error("Некорректный номер")
				continue
			}
			if err := subscription.RemoveSubscription(delIdx - 1); err != nil {
				ui.Error(fmt.Sprintf("Ошибка удаления: %v", err))
				continue
			}
			ui.Success(fmt.Sprintf("Подписка «%s» удалена", subs[delIdx-1].Name))
			subs, _ = subscription.LoadSubscriptions()
			continue
		case input == "0":
			os.Exit(0)
		default:
			idx, err := strconv.Atoi(input)
			if err != nil || idx < 1 || idx > len(subs) {
				ui.Error("Некорректный номер")
				continue
			}
			ui.Success(fmt.Sprintf("Подписка: %s", subs[idx-1].Name))
			return subs[idx-1].URL
		}
	}
}

func (a *App) addSubFlow() error {
	ui.Title("Добавление подписки")

	rawURL, err := ui.StyledInput("Вставьте URL подписки")
	if err != nil {
		return fmt.Errorf("ввод отменён")
	}
	if rawURL == "" {
		return fmt.Errorf("URL не может быть пустым")
	}

	// S-2: reject anything that isn't an http(s) subscription URL outright
	// instead of warning and saving it anyway.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("некорректный URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL должен начинаться с http:// или https:// (получено %q)", parsed.Scheme)
	}
	if parsed.Scheme == "http" {
		ui.Warn("URL использует http без шифрования — предпочтительнее https")
	}

	ui.Warn("Проверка URL идёт напрямую, вне VPN-туннеля")
	ui.Progress("Проверка URL")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, headErr := client.Head(rawURL)
	ui.ClearLine()
	if headErr != nil {
		ui.Warn(fmt.Sprintf("Не удалось проверить URL: %v (всё равно сохраню)", headErr))
	} else {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			ui.Success("URL доступен")
		} else {
			ui.Warn(fmt.Sprintf("URL вернул HTTP %d (всё равно сохраню)", resp.StatusCode))
		}
	}

	if err := subscription.SaveSubscription(rawURL); err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}

	ui.Success("Подписка добавлена")
	return nil
}

func (a *App) fetchAndSelectServer(ctx context.Context, subURL string, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	ui.Progress("Загрузка подписки")
	hwid := config.GetOrCreateHWID(a.cfg.HWID)
	entries, err := subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	ui.ClearLine()
	if err != nil {
		return nil, fmt.Errorf("загрузка подписки: %w", err)
	}
	slog.Info("subscription loaded", "servers", len(entries))
	ui.Success(fmt.Sprintf("Загружено %d серверов", len(entries)))

	// A-4: refresh re-fetches with the same HWID headers as the initial load.
	refresh := func() ([]subscription.SubEntry, error) {
		return subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	}

	pb := NewProxyBenchmarker(tc, a.binary, 3, 8*time.Second, a.cfg.AllowInsecure)
	selected := subscription.ShowMenu(ctx, entries, refresh, pb.Run)
	if selected == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("switch subscription")
	}

	if err := selected.Validate(); err != nil {
		return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
	}
	selected.AllowInsecure = a.cfg.AllowInsecure
	a.printSubEntryDetails(selected)
	a.setServerEndpoint(selected)
	resolveServer(selected.Address)
	return subscription.BuildOutboundJSON(selected)
}

// setServerEndpoint records the selected server so the kill switch can allow
// xray's own traffic to it (H-1). hysteria2 uses UDP; everything else TCP.
func (a *App) setServerEndpoint(e *subscription.SubEntry) {
	a.serverHost = e.Address
	a.serverPort = e.Port
	a.serverUDP = e.Protocol == "hysteria2"
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

	tc, err := xraycfg.LoadTemplate("template.json")
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}

	// H-3: resolve the binary up front (before the benchmarker needs it) and
	// fail fast with a clear error if xray is missing.
	binary, err := xray.FindBinary()
	if err != nil {
		return err
	}
	a.binary = binary

	// X-4: log the xray version; incompatibility is diagnosed here, not via retries.
	if v, err := xray.Version(binary); err == nil {
		slog.Info("xray version", "version", v)
	} else {
		slog.Warn("could not determine xray version", "error", err)
	}

	proxyOutbound, err := a.resolveOutbound(ctx, tc)
	if err != nil {
		return err
	}

	if a.cfg.Mode == "tun" {
		tc.Inbounds = []xraycfg.Inbound{xraycfg.BuildTUNInbound()}
		fmt.Println("  Режим: TUN (весь трафик через VPN)")
		slog.Info("mode", "mode", "tun")
	} else {
		fmt.Println("  Режим: Прокси (HTTP/SOCKS5)")
		slog.Info("mode", "mode", "proxy")
	}

	cfg := xraycfg.MergeConfig(tc, proxyOutbound)
	cfg.Log = &xraycfg.LogConfig{Loglevel: a.cfg.XrayLogLvl}

	// R-3: refuse to start a second instance racing over the same config/ports.
	if err := a.acquireLock(); err != nil {
		return err
	}
	if a.cfg.Mode != "tun" {
		socksPort, httpPort, err := a.resolvePorts(cfg)
		if err != nil {
			return err
		}
		for _, p := range []int{socksPort, httpPort} {
			if portInUse(p) {
				return fmt.Errorf("порт %d уже занят другим процессом — возможно, запущен второй экземпляр", p)
			}
		}
	}

	if err := a.writeConfig(cfg); err != nil {
		return err
	}

	printConfigSummary(cfg, a.cfg.Mode)
	fmt.Println("── Routing ──────────────────────────────")
	printRoutingRules(cfg)

	slog.Info("starting xray", "binary", binary, "mode", a.cfg.Mode)

	a.runner = xray.New(binary, a.tmpFile)

	// X-1: validate the config up front; a test failure is a deterministic
	// error and must not be retried.
	if err := a.runner.TestConfig(ctx); err != nil {
		return fmt.Errorf("проверка конфигурации xray не прошла: %w", err)
	}

	xrayCtx, xrayCancel := context.WithCancel(ctx)
	defer xrayCancel()

	var xrayDone sync.WaitGroup
	xrayDone.Add(1)
	go func() {
		defer xrayDone.Done()
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
	xrayDone.Wait()
	return err
}

func (a *App) runProxy(ctx context.Context, cfg *xraycfg.XrayConfig) error {
	socksPort, httpPort, err := a.resolvePorts(cfg)
	if err != nil {
		return err
	}
	slog.Info("proxy listening", "socks5", socksPort, "http", httpPort)

	if !awaitPort(ctx, socksPort, "SOCKS5", 10*time.Second) {
		return fmt.Errorf("SOCKS5 порт не открылся за 10с")
	}
	if !awaitPort(ctx, httpPort, "HTTP", 5*time.Second) {
		return fmt.Errorf("HTTP порт не открылся за 5с")
	}

	testProxyConnection(ctx, httpPort)

	a.originalProxy = system.ReadProxyState()

	if err := a.proxy.Enable(httpPort); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
	} else {
		a.proxyTouched = true
		slog.Info("system proxy enabled", "port", httpPort)
		fmt.Println("✅ Системный прокси включён (127.0.0.1:" + strconv.Itoa(httpPort) + ")")
		verifySystemProxy(httpPort)
	}

	fmt.Println("──────────────────────────────────────────")
	go a.healthCheckLoopPorts(ctx, socksPort, httpPort)

	<-ctx.Done()
	slog.Info("shutting down xray")
	return nil
}

func (a *App) runTun(ctx context.Context, cfg *xraycfg.XrayConfig) error {
	fmt.Print("  ⏳ Ожидание TUN интерфейса... ")
	if !a.awaitTUNInterface(ctx, xraycfg.TunInterfaceName, 10*time.Second) {
		fmt.Println("❌ не поднялся")
		slog.Error("tun interface did not come up")
		return fmt.Errorf("TUN-интерфейс %s не поднялся за 10с", xraycfg.TunInterfaceName)
	}
	fmt.Println("✅ TUN активирован")
	slog.Info("tun interface activated")

	if a.cfg.KillSwitch {
		fmt.Print("  ⛔ Включение Kill Switch... ")
		ksCfg := system.KillSwitchConfig{
			ServerIP:   resolveFirstIP(a.serverHost),
			ServerPort: a.serverPort,
			UDP:        a.serverUDP,
			XrayPath:   a.binary,
		}
		if err := system.EnableKillSwitch(ksCfg); err != nil {
			fmt.Printf("❌ %v\n", err)
			slog.Warn("kill switch failed", "error", err)
		} else {
			fmt.Println("✅")
			slog.Info("kill switch enabled")
		}
	}

	testConnectivity(ctx)
	fmt.Println("──────────────────────────────────────────")
	go a.healthCheckLoopConnectivity(ctx)

	<-ctx.Done()
	slog.Info("shutting down xray")
	return nil
}

func (a *App) awaitTUNInterface(ctx context.Context, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		ifaces, err := a.interfaces()
		if err == nil {
			for _, iface := range ifaces {
				if iface.Name == name && iface.Flags&net.FlagUp != 0 {
					return true
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
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
		time.Sleep(delay)
	}
	fmt.Println("  🌐 Проверка подключения... ❌ не удалась")
	slog.Error("connectivity check failed after 3 attempts")
}

func (a *App) cleanup() {
	if a.cfg != nil && a.cfg.Mode == "tun" {
		system.DisableKillSwitch()
	} else {
		if a.proxyTouched {
			if err := a.proxy.Restore(a.originalProxy); err != nil {
				slog.Warn("failed to restore proxy", "error", err)
			}
		}
	}
	if a.runner != nil {
		_ = a.runner.Stop()
	}
	os.Remove(a.tmpFile)
	if a.lockHeld {
		os.Remove(a.lockFile)
	}
}

// acquireLock creates an exclusive lock file next to the config so a second
// instance from the same directory refuses to start (R-3).
func (a *App) acquireLock() error {
	f, err := os.OpenFile(a.lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("обнаружен lock-файл %s — возможно, уже запущен другой экземпляр; удалите файл, если это не так", a.lockFile)
		}
		return fmt.Errorf("создание lock-файла: %w", err)
	}
	f.Close()
	a.lockHeld = true
	return nil
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

// resolveFirstIP resolves host to a single IP for the kill-switch server rule.
// It runs before the kill switch is active, so DNS still works.
func resolveFirstIP(host string) string {
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return host
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		slog.Warn("kill switch: could not resolve server, no server exception added", "host", host, "error", err)
		return ""
	}
	return addrs[0]
}

func (a *App) writeConfig(cfg *xraycfg.XrayConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// H-2: the config holds UUIDs, passwords and keys — keep it owner-only, and
	// never write the full (unmasked) config to the log, only its path.
	if err := os.WriteFile(a.tmpFile, data, 0600); err != nil {
		return err
	}
	slog.Debug("generated xray config", "path", a.tmpFile)
	return nil
}

// resolvePorts finds the SOCKS and HTTP inbound ports by protocol/tag rather
// than by position, so reordering inbounds in the template can't silently swap
// them (Q-2).
func (a *App) resolvePorts(cfg *xraycfg.XrayConfig) (socksPort, httpPort int, err error) {
	for _, in := range cfg.Inbounds {
		switch {
		case in.Protocol == "socks" || in.Tag == "socks":
			socksPort = in.Port
		case in.Protocol == "http" || in.Tag == "http":
			httpPort = in.Port
		}
	}
	if socksPort == 0 || httpPort == 0 {
		return 0, 0, fmt.Errorf("в конфиге не найдены socks/http inbound-порты (socks=%d http=%d)", socksPort, httpPort)
	}
	return socksPort, httpPort, nil
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
		slog.Error("dns resolve failed", "host", host, "error", err)
		return
	}
	if len(addrs) == 0 {
		fmt.Println("❌ Нет записей A/AAAA")
		slog.Warn("dns resolve returned no records", "host", host)
		return
	}
	fmt.Printf("✅ %s → %s\n", host, strings.Join(addrs, ", "))
	slog.Info("dns resolved", "host", host, "addrs", addrs)
}

func (a *App) printSubEntryDetails(e *subscription.SubEntry) {
	fmt.Println("── Выбранный сервер ─────────────────────")
	fmt.Printf("  Протокол:  %s\n", e.Protocol)
	fmt.Printf("  Сервер:    %s\n", e.Address)
	fmt.Printf("  Порт:      %d\n", e.Port)
	if e.UUID != "" {
		fmt.Printf("  UUID:      %s\n", a.maskIfNeeded(e.UUID))
	}
	fmt.Printf("  Transport: %s\n", e.Network)
	if e.Security != "" {
		fmt.Printf("  Security:  %s\n", e.Security)
	}
	if e.Remarks != "" {
		fmt.Printf("  Заметка:   %s\n", e.Remarks)
	}
	if e.ShortID != "" {
		fmt.Printf("  ShortID:   %s\n", a.maskIfNeeded(e.ShortID))
	}
	// S-1: warn when the subscription requested skipping TLS verification.
	if e.Insecure {
		if a.cfg.AllowInsecure {
			ui.Warn("Сервер запросил insecure — проверка TLS-сертификата ОТКЛЮЧЕНА (ALLOW_INSECURE=true)")
		} else {
			ui.Warn("Сервер запросил insecure — флаг проигнорирован (задайте ALLOW_INSECURE=true, чтобы разрешить)")
		}
	}
	fmt.Println("─────────────────────────────────────────")
}

func (a *App) printURLDetails(u *url.URL) {
	fmt.Println("── URL ──────────────────────────────────")
	fmt.Printf("  Протокол:  %s\n", u.Scheme)
	host, port, _ := net.SplitHostPort(u.Host)
	fmt.Printf("  Сервер:    %s\n", host)
	fmt.Printf("  Порт:      %s\n", port)
	if u.Scheme == "vless" {
		q := u.Query()
		fmt.Printf("  UUID:      %s\n", a.maskIfNeeded(u.User.Username()))
		printParam(q, "type", "Transport")
		printParam(q, "security", "Security")
		printParam(q, "sni", "SNI")
		printParam(q, "fp", "Fingerprint")
		if v := q.Get("pbk"); v != "" {
			fmt.Printf("  PublicKey: %s\n", a.maskIfNeeded(v))
		}
		if v := q.Get("sid"); v != "" {
			fmt.Printf("  ShortID:   %s\n", a.maskIfNeeded(v))
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

func (a *App) maskIfNeeded(s string) string {
	return maskString(s, a.cfg.MaskCreds)
}

// maskURL hides the personal token in a subscription URL, keeping only the
// host and the first few path characters for recognizability (S-3).
func maskURL(raw string, mask bool) string {
	if !mask || raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return maskString(raw, true)
	}
	path := u.Path
	if len(path) > 6 {
		path = path[:6] + "…"
	} else if path != "" {
		path += "…"
	}
	return u.Scheme + "://" + u.Host + path
}

func maskString(s string, mask bool) string {
	if !mask || s == "" {
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
			slog.Warn("port wait cancelled", "label", label, "port", port)
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Printf("✅ %s доступен\n", label)
			slog.Info("port available", "label", label, "port", port)
			return true
		}
		time.Sleep(200 * time.Millisecond)
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
		time.Sleep(delay)
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
