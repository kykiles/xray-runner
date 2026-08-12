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
	"strconv"
	"strings"
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
	mode          string // active mode; starts at cfg.Mode or the saved mode, toggled by the m key
	modeFromState bool   // mode came from the saved state, not from cfg.Mode
	pendingNote   string // shown on the next status screen (e.g. a mode fallback)
	noTTY         bool   // stdin is not a terminal (systemd, pipe): no screen to draw on
	nav           nav    // menu position, kept across sessions
	serverHost    string
	serverPort    int
	serverUDP     bool
	originalProxy system.ProxyState
	proxyTouched  bool
	tunRouted     bool // tun routes installed; teardown must remove them
	killSwitchOn  bool // kill switch enabled; teardown must take it down
	// Split tunnelling (proxy mode): the names read from apps.txt, the ones that
	// were actually running, and whether teardown has anything to undo.
	splitApps    []string
	splitMatched []string
	splitOn      bool
	interfaces   func() ([]net.Interface, error)
	// disableKillSwitch / restoreProxy are the teardown side of the two system
	// changes a session makes; fields so tests can observe them without touching
	// the machine's firewall or proxy settings.
	disableKillSwitch func() error
	restoreProxy      func(system.ProxyState) error
	disableSplit      func() error
	// checkPrivileges reports whether tun mode may be used; a field so tests can
	// exercise both outcomes without being root.
	checkPrivileges func() error
	// pidAlive reports whether the process that owns an existing lock file is
	// still running; a field so tests can stub liveness deterministically.
	pidAlive func(pid int) bool
	// healthLoop overrides the mode's health loop; a field so tests can observe
	// the loop's lifetime without a network or a running core.
	healthLoop func(context.Context, sessionPorts)
	// showStatus draws the status screen; a field so tests can drive the session
	// loop without a terminal.
	showStatus func(tui.StatusInfo, <-chan tui.StatusUpdate) (tui.StatusAction, error)
	// connectScreen draws the bring-up screen; a field so tests can drive the
	// cancellation path without a terminal.
	connectScreen func(title string, connect func() error, cancel func()) error

	// U-5: status data, written by health loops, read by the status screen.
	statusMu    sync.Mutex
	lastCheck   time.Time
	lastCheckOK bool
	statusCh    chan tui.StatusUpdate
}

func New(cfg *config.Config, opts Options) *App {
	proxy := system.New()
	return &App{
		cfg:               cfg,
		opts:              opts,
		mode:              cfg.Mode,
		proxy:             proxy,
		tmpFile:           config.Path("xray_config.json"),
		lockFile:          config.Path("xray_config.json.lock"),
		interfaces:        net.Interfaces,
		checkPrivileges:   checkTunPrivileges,
		pidAlive:          processAlive,
		noTTY:             !term.IsTerminal(int(os.Stdin.Fd())),
		disableKillSwitch: system.DisableKillSwitch,
		restoreProxy:      proxy.Restore,
		disableSplit:      system.DisableSplit,
		showStatus:        tui.ShowStatus,
		connectScreen:     tui.ShowConnecting,
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

	if err := a.resolveMode(); err != nil {
		return err
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

	// The geo databases sit next to the core, and the panel's routing may name
	// lists they do not carry — see xraycfg.SetGeoAssets.
	xraycfg.SetGeoAssets(filepath.Dir(binary))

	// X-4: log the xray version; incompatibility is diagnosed here, not via retries.
	v, err := xray.Version(binary)
	if err == nil {
		slog.Info("xray version", "version", v)
	} else {
		slog.Warn("could not determine xray version", "error", err)
	}

	// The split mode is a routing rule the core has to understand. An older core
	// starts on the config and quietly ignores the rule, which would put every
	// process in the tunnel — the opposite of what the mode promises.
	if a.splitMode() && system.SplitOverTUN && err == nil && !xray.SupportsProcessRouting(v) {
		return fmt.Errorf("режим SPLIT требует xray-core 26.0 или новее, установлен: %s", v)
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
	if a.noTTY {
		return fmt.Errorf("%w: нет терминала для интерактивного выбора — используйте --server, --last или --non-interactive", ErrSelection)
	}

	if err := a.migrateLegacyURL(); err != nil {
		return err
	}

	return a.menuLoop(ctx)
}

// resolveMode settles which mode this run starts in, before anything is built
// on top of the answer.
//
// R-5: tun mode needs root/elevation — decide here with a clear message instead
// of letting xray retry for 30 seconds.
func (a *App) resolveMode() error {
	a.applySavedMode()
	if !a.tunMode() {
		return nil
	}
	err := a.checkPrivileges()
	if err == nil {
		return nil
	}
	// A mode the user asked for explicitly (MODE=tun) is a demand, so it still
	// fails loudly. A remembered mode is only a preference: falling back beats
	// refusing to start over a choice made last time.
	if !a.modeFromState {
		return err
	}
	slog.Warn("saved mode unavailable, falling back to proxy", "mode", a.mode, "error", err)
	a.pendingNote = "Прошлый режим " + strings.ToUpper(a.mode) + " недоступен: " + err.Error() + ". Работаем в PROXY."
	a.mode = "proxy"
	return nil
}

// tunMode reports whether this session is built on the TUN interface: the tun
// mode itself, and — where per-process routing is done by xray's own process
// matching rather than by the firewall — the split mode too (ADR-0003).
func (a *App) tunMode() bool { return tunBased(a.mode) }

// tunBased answers the same question for a mode the app has not switched to yet.
func tunBased(mode string) bool {
	return mode == "tun" || (mode == "split" && system.SplitOverTUN)
}

// splitMode reports whether only the listed processes travel the tunnel.
func (a *App) splitMode() bool { return a.mode == "split" }

// applySavedMode brings the app up in the mode the user last switched to,
// overriding cfg.Mode: MODE in .env seeds the first run, after that the m key is
// what the user expects to be remembered. A mode that is saved but unusable is
// left on disk untouched — the fallback in Run is for this run only, so a
// restart with sudo returns to tun without re-selecting it.
func (a *App) applySavedMode() {
	s, err := loadLastState()
	if err != nil || s.Mode == "" {
		return
	}
	if !config.ValidMode(s.Mode) {
		slog.Warn("saved mode is not a known mode, ignoring", "mode", s.Mode)
		return
	}
	if s.Mode != a.mode {
		slog.Info("restored mode from the last session", "mode", s.Mode, "env_mode", a.cfg.Mode)
	}
	a.mode = s.Mode
	a.modeFromState = true
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
				// On the normal buffer, so the message is still there when the app
				// exits instead of being wiped by the next screen.
				tui.ReleaseScreen()
				ui.Error(err.Error())
				a.nav.level = levelList
				break
			}

			switch action {
			case tui.StatusQuit:
				return ErrUserQuit
			case tui.StatusBack:
				// Resume at the screen the target was picked on.
				a.nav.level = levelList
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
	// The menus keep the alternate buffer up between screens; the shell gets it
	// back here, before main prints its goodbye.
	tui.ReleaseScreen()
	a.releaseSession()
	_ = os.Remove(a.tmpFile)
	if a.lockHeld {
		_ = os.Remove(a.lockFile)
	}
}

// acquireLock creates an exclusive lock file next to the config so a second
// instance from the same directory refuses to start (R-3). An existing lock left
// behind by a crashed or killed run (a "stale" lock) is reclaimed automatically:
// the file carries the owner's pid, and if that process is gone we remove the
// lock and take it over instead of forcing the user to delete the file by hand.
func (a *App) acquireLock() error {
	err := a.createLock()
	if err == nil {
		return nil
	}
	if !os.IsExist(err) {
		return fmt.Errorf("создание lock-файла: %w", err)
	}

	// A lock already exists. If its owner is still running, this is a genuine
	// second instance and we refuse. Otherwise the lock is stale — reclaim it.
	if pid, ok := readLockPID(a.lockFile); ok && a.pidAlive(pid) {
		return fmt.Errorf("обнаружен lock-файл %s — уже запущен другой экземпляр (pid %d)", a.lockFile, pid)
	}

	slog.Warn("reclaiming stale lock file", "file", a.lockFile)
	if err := os.Remove(a.lockFile); err != nil {
		return fmt.Errorf("удаление протухшего lock-файла %s: %w", a.lockFile, err)
	}
	if err := a.createLock(); err != nil {
		return fmt.Errorf("создание lock-файла: %w", err)
	}
	return nil
}

// createLock atomically creates the lock file and records our pid in it, so a
// later run can tell a live owner from a stale one.
func (a *App) createLock() error {
	f, err := os.OpenFile(a.lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	if err := f.Close(); err != nil {
		return err
	}
	a.lockHeld = true
	return nil
}

// readLockPID reads the owner pid from an existing lock file. A missing, empty
// or unparseable file returns ok=false, which callers treat as a stale lock.
func readLockPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// coreVersion returns the installed xray core version number (e.g. "26.6.27"),
// or "" if the binary cannot be queried. It feeds the update screen so the user
// can see which core they are currently running.
func coreVersion(binary string) string {
	line, err := xray.Version(binary)
	if err != nil {
		return ""
	}
	return parseCoreVersion(line)
}

// parseCoreVersion pulls the version number out of the first line of
// `xray version`, e.g. "Xray 26.6.27 (…)" -> "26.6.27".
func parseCoreVersion(line string) string {
	if fields := strings.Fields(line); len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// resolveAllIPs resolves every host to every one of its addresses, for the TUN
// route exceptions and the kill-switch server rules. One address per host is
// not enough: xray may dial any record the name resolves to, and a missed one
// would route the tunnel's own uplink back into the tunnel — or, for the kill
// switch, see it dropped. It must run before the routes are installed, while
// DNS still takes the physical path.
func resolveAllIPs(hosts []string) []string { return resolveIPs(hosts, false) }

// resolveAllIPs6 is the same for IPv6, used where the tunnel claims that family
// too. The two are kept apart because an exception route belongs to exactly one
// of them: a v6 address handed to route.exe is an error, and a v4 one handed to
// netsh interface ipv6 is another.
func resolveAllIPs6(hosts []string) []string { return resolveIPs(hosts, true) }

func resolveIPs(hosts []string, v6 bool) []string {
	var ips []string
	seen := map[string]bool{}
	// A literal address is filtered by family exactly like a resolved one: an
	// entry of the wrong family is not an exception the routing layer can write.
	wanted := func(addr string) bool {
		ip := net.ParseIP(addr)
		return ip != nil && (ip.To4() == nil) == v6 && !seen[addr]
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if net.ParseIP(host) != nil {
			if wanted(host) {
				seen[host] = true
				ips = append(ips, host)
			}
			continue
		}
		addrs, err := net.LookupHost(host)
		if err != nil || len(addrs) == 0 {
			slog.Warn("tun routing: could not resolve server", "host", host, "error", err)
			continue
		}
		for _, a := range addrs {
			if !wanted(a) {
				continue
			}
			seen[a] = true
			ips = append(ips, a)
		}
	}
	return ips
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
