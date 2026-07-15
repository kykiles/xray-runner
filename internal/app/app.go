package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/term"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/ui"
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
	template      *xraycfg.XrayConfig
	mode          string // active mode; starts at cfg.Mode, toggled by the m key
	nav           nav    // menu position, kept across sessions
	serverHost    string
	serverPort    int
	serverUDP     bool
	originalProxy system.ProxyState
	proxyTouched  bool
	interfaces    func() ([]net.Interface, error)

	// U-5: status data, written by health loops, read by the status screen.
	statusMu    sync.Mutex
	lastCheck   time.Time
	lastCheckOK bool
	statusCh    chan tui.StatusUpdate
}

func New(cfg *config.Config, opts Options) *App {
	return &App{
		cfg:        cfg,
		opts:       opts,
		mode:       cfg.Mode,
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
	if a.mode == "tun" {
		if err := checkTunPrivileges(); err != nil {
			return err
		}
	}

	tc, err := xraycfg.LoadTemplate("template.json")
	if err != nil {
		return fmt.Errorf("load template: %w", err)
	}
	a.template = tc

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

	// R-3: refuse to start a second instance racing over the same config/ports.
	if err := a.acquireLock(); err != nil {
		return err
	}

	// U-2: scripted selection runs exactly one session and never shows a menu.
	if a.opts.Server != "" || a.opts.UseLast || a.opts.NonInteractive {
		t, err := a.resolveScriptedTarget()
		if err != nil {
			return err
		}
		_, err = a.runSession(ctx, t)
		return err
	}

	// U-3: the TUI needs a terminal; without one, only scripted selection works.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("%w: нет терминала для интерактивного выбора — используйте --server, --last или --non-interactive", ErrSelection)
	}

	if err := a.migrateLegacyURL(); err != nil {
		return err
	}

	return a.menuLoop(ctx)
}

// menuLoop alternates between the menu and a connected session: the session
// screen can send the user back to the server list (esc) or restart the core in
// the other mode (m), which is why selection is not a one-shot step.
func (a *App) menuLoop(ctx context.Context) error {
	for {
		t, err := a.chooseTarget(ctx)
		if err != nil {
			return err
		}

		for {
			action, err := a.runSession(ctx, t)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, ErrUserQuit) {
					return err
				}
				// A failed session must not kill the app: report it and let the
				// user pick another server.
				slog.Error("session failed", "error", err)
				ui.Error(err.Error())
				a.nav.level = levelServers
				break
			}

			switch action {
			case tui.StatusQuit:
				return ErrUserQuit
			case tui.StatusBack:
				// Resume at the server list of the same profile.
				a.nav.level = levelServers
			case tui.StatusSwitchMode, tui.StatusRestart:
				continue
			}
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// cleanup runs once at exit. Per-session teardown already happened in
// releaseSession; this is the last-resort net for a session that failed midway.
func (a *App) cleanup() {
	a.releaseSession()
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

// writeConfigJSON stores the generated config for xray to read.
func (a *App) writeConfigJSON(raw json.RawMessage) error {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// H-2: the config holds UUIDs, passwords and keys — keep it owner-only, and
	// never write the full (unmasked) config to the log, only its path.
	if err := os.WriteFile(a.tmpFile, pretty.Bytes(), 0600); err != nil {
		return err
	}
	slog.Debug("generated xray config", "path", a.tmpFile)
	return nil
}
