package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// Control-flow sentinels (A-2/R-4). Using typed errors instead of string
// comparison keeps the "back to subscription list" / "user quit" signals robust
// and lets main perform a single clean exit with cleanup.
var (
	ErrSwitchSubscription = errors.New("switch subscription")
	ErrUserQuit           = errors.New("user quit")
	// ErrSelection marks non-interactive selection/usage failures so main can
	// exit with a distinct code for scripts/systemd (U-2).
	ErrSelection = errors.New("selection failed")
)

// Options are CLI-level knobs (U-2): pick a server without the menu.
type Options struct {
	Server         string // index (1-based) or name of the server to use
	UseLast        bool   // reuse the last selected subscription/server
	NonInteractive bool   // never prompt; fail instead
}

type App struct {
	cfg           *config.Config
	opts          Options
	runner        *xray.Runner
	proxy         *system.ProxyManager
	tmpFile       string
	lockFile      string
	lockHeld      bool
	binary        string
	serverHost    string
	serverPort    int
	serverUDP     bool
	originalProxy system.ProxyState
	proxyTouched  bool
	interfaces    func() ([]net.Interface, error)
}

func New(cfg *config.Config, opts Options) *App {
	return &App{
		cfg:        cfg,
		opts:       opts,
		proxy:      system.New(),
		tmpFile:    filepath.Join(".", "xray_config.json"),
		lockFile:   filepath.Join(".", "xray_config.json.lock"),
		interfaces: net.Interfaces,
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

	// R-5: tun mode needs root/elevation — fail immediately with a clear
	// message instead of letting xray retry for 30 seconds.
	if a.cfg.Mode == "tun" {
		if err := checkTunPrivileges(); err != nil {
			return err
		}
	}

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
