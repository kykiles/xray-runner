package app

import (
	"context"
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
	"xray-runner/internal/subscription"
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
	lockFile      string
	lock          *os.File // lockFile, open while this instance holds its lock
	binary        string
	template      *xraycfg.XrayConfig
	mode          string // active mode (proxy/tun); starts at cfg.Mode or the saved mode, toggled by the m key
	modeFromState bool   // mode came from the saved state, not from cfg.Mode
	split         bool   // this session routes only the processes from apps.txt (derived, see resolveSplit)
	coreVer       string // xray version string, empty when it could not be read
	pendingNote   string // shown on the next status screen (e.g. a mode fallback)
	geoNote       string // why the geo databases in use are not the panel's current ones
	noTTY         bool   // stdin is not a terminal (systemd, pipe): no screen to draw on
	nav           nav    // menu position, kept across sessions
	serverHost    string
	serverPort    int
	serverUDP     bool
	originalProxy system.ProxyState
	proxyTouched  bool
	tunRouted     bool // tun routing attempted; teardown must take ours back out
	// The active config routes something past the tunnel — as a rule the panel
	// profile's own direct rules. The TUN label says so instead of promising
	// that every packet goes through the VPN (A03).
	hasBypass    bool
	killSwitchOn bool // kill switch enabled; teardown must take it down
	// directBound is where the session's config sends direct traffic past the
	// tunnel (buildSessionConfig); a restarted core is not routed under a stale one.
	directBound xraycfg.DirectBind
	// Split tunnelling (proxy mode): the names read from apps.txt, the ones that
	// were actually running, and whether teardown has anything to undo.
	splitApps    []string
	splitMatched []string
	splitOn      bool
	// splitUnclosed: the running ones whose connections from before the move
	// still go past the tunnel, as the last scan reported them (14b).
	splitUnclosed []string
	interfaces    func() ([]net.Interface, error)
	// disableKillSwitch / restoreProxy are the teardown side of the two system
	// changes a session makes; fields so tests can observe them without touching
	// the machine's firewall or proxy settings.
	disableKillSwitch func() error
	restoreProxy      func(system.ProxyState) error
	// readProxyState / proxyListening feed the check for a proxy left on a dead
	// local port (warnDeadLoopbackProxy), fields so tests need neither the
	// desktop's settings nor the network.
	readProxyState func() system.ProxyState
	proxyListening func(addr string) bool
	// clearProxy takes a leftover proxy setting of ours off the machine
	// (clearDeadProxy); a field for the same reason as restoreProxy.
	clearProxy func() error
	// recoverJournal takes down the routes, kill switch and split a run that
	// died without its teardown left behind (recoverLeftovers); a field for the
	// same reason.
	recoverJournal func() (string, error)
	// enableKillSwitch / enableTunRouting / disableTunRouting are the tun
	// bring-up's system changes, fields for the same reason.
	enableKillSwitch  func(system.KillSwitchConfig) error
	enableTunRouting  func(system.TunRouteConfig) error
	disableTunRouting func() error
	// enableSplit / rescanSplit / disableSplit are the split's system side,
	// fields so tests can feed it scans without root, nft or /proc.
	enableSplit  func(names []string, tcpPort, dnsPort int) (system.SplitScan, error)
	rescanSplit  func(names []string) (system.SplitScan, error)
	disableSplit func() error
	// directBind reports how the TUN config's freedom outbounds leave past the
	// tunnel; a field because on Windows the answer comes from the machine's
	// physical adapter, which a unit test of the config must not depend on.
	directBind func() (xraycfg.DirectBind, error)
	// checkPrivileges reports whether tun mode may be used; a field so tests can
	// exercise both outcomes without being root.
	checkPrivileges func() error
	// healthLoop overrides the mode's health loop; a field so tests can observe
	// the loop's lifetime without a network or a running core.
	healthLoop func(context.Context, sessionPorts)
	// healthProbe replaces the health loop's check through the core's inbound; a
	// field so tests can see when a session is checked, without a network.
	healthProbe func() (bool, time.Duration)
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
	// healthGen is the core whose health results may reach the screen: 0 for a
	// proxy session's own loop, the core's number in a tun session (C01).
	healthGen int
	statusCh  chan tui.StatusUpdate

	// remote is the connection to the service, when there is one (H10).
	remote remoteState
}

func New(cfg *config.Config, opts Options) *App {
	proxy := system.New()
	// The lock stays where 11a put it, beside where config.Path once found the
	// core's config, so the runs of one install still meet at one lock. The
	// config itself is no file at all now: the core reads it on stdin (H01).
	return &App{
		cfg:               cfg,
		opts:              opts,
		mode:              cfg.Mode,
		proxy:             proxy,
		lockFile:          config.Path("xray_config.json") + ".lock",
		interfaces:        net.Interfaces,
		checkPrivileges:   checkTunPrivileges,
		noTTY:             !term.IsTerminal(int(os.Stdin.Fd())),
		disableKillSwitch: system.DisableKillSwitch,
		readProxyState:    system.ReadProxyState,
		proxyListening:    listeningAt,
		restoreProxy:      proxy.Restore,
		clearProxy:        system.ClearProxy,
		recoverJournal:    system.RecoverJournal,
		enableKillSwitch:  system.EnableKillSwitch,
		enableTunRouting:  system.EnableTunRouting,
		disableTunRouting: system.DisableTunRouting,
		enableSplit:       system.EnableSplit,
		rescanSplit:       system.RefreshSplit,
		disableSplit:      system.DisableSplit,
		directBind:        system.DirectBind,
		showStatus:        tui.ShowStatus,
		connectScreen:     tui.ShowConnecting,
	}
}

func (a *App) Run(ctx context.Context) error {
	defer a.cleanup()

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
	xraycfg.SetGeoAssets(filepath.Dir(binary), "")

	// X-4: log the xray version; incompatibility is diagnosed here, not via retries.
	v, err := xray.Version(binary)
	if err == nil {
		slog.Info("xray version", "version", v)
		a.coreVer = v
	} else {
		slog.Warn("could not determine xray version", "error", err)
	}

	// The service, when there is one, is what makes TUN possible without
	// elevation, so it is asked before the mode is settled.
	a.useService(ctx)

	// Whether TUN can run depends on the core's version, so the mode is settled
	// only once that is known.
	if err := a.resolveMode(); err != nil {
		return err
	}

	// R-3: refuse to start a second instance racing over the same ports.
	if err := a.acquireLock(); err != nil {
		return err
	}
	a.recoverLeftovers()
	a.noteSubscriptionStorage()

	// U-2: scripted selection runs exactly one session and never shows a menu.
	if a.opts.Server != "" || a.opts.UseLast || a.opts.NonInteractive {
		t, err := a.resolveScriptedTarget(ctx)
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
	err := a.tunReady()
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
// matching rather than by the firewall — split routing too (ADR-0003).
func (a *App) tunMode() bool { return a.mode == "tun" || (a.split && system.SplitOverTUN) }

// resolveSplit decides whether this session routes only the processes from
// apps.txt. Split is not a mode the user picks: it is what proxy mode does when
// there is a list to route and the platform can route it — the same rule on
// both systems, so there is no third MODE value to know about.
//
// Anything missing degrades to plain proxy with a note: refusing to connect
// over a routing extra would cost the user the working connection too.
func (a *App) resolveSplit() {
	a.split = a.mode == "proxy" && len(a.splitApps) > 0
	if !a.split || !system.SplitOverTUN {
		return
	}
	// Where split rides on the tunnel it needs everything TUN needs.
	if err := a.tunReady(); err != nil {
		a.splitOff(err.Error())
	}
}

// tunReady reports whether a session built on the TUN interface may start: it
// needs elevation and a core at xray.MinVersion or newer. An unknown version is
// let through, the same as an unreadable one.
func (a *App) tunReady() error {
	if c := a.service(); c != nil {
		return a.serviceCoreReady(c)
	}
	if err := a.checkPrivileges(); err != nil {
		return err
	}
	if a.coreVer != "" && !xray.MeetsMinVersion(a.coreVer) {
		return fmt.Errorf("нужен xray-core %s или новее, установлен %s", xray.MinVersion, parseCoreVersion(a.coreVer))
	}
	return nil
}

// noteSubscriptionStorage says once, on the first status screen, that the
// subscription list is kept unsealed and why: no Secret Service on this
// machine, typically a headless server.
func (a *App) noteSubscriptionStorage() {
	note := subscription.StorageNote()
	if note == "" {
		return
	}
	slog.Warn("subscriptions stored unsealed", "reason", note)
	if a.pendingNote != "" {
		note = a.pendingNote + " " + note
	}
	a.pendingNote = note
}

// splitOff drops back to plain proxy and says why — on screen once, in the log
// for good.
func (a *App) splitOff(reason string) {
	a.split = false
	slog.Warn("split routing off, staying in proxy", "reason", reason, "apps", len(a.splitApps))
	a.pendingNote = "Маршрутизация по процессам выключена: " + reason
}

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
	a.closeService()
	// Then the lock; its handle goes with it, so a second cleanup cannot free
	// the next owner's. The lock file itself stays — see acquireLock.
	if a.lock != nil {
		_ = unlock(a.lock)
		_ = a.lock.Close()
		a.lock = nil
	}
}

// errLockBusy is tryLock's answer when another open file holds the lock.
var errLockBusy = errors.New("lock held by another open file")

// acquireLock takes an OS lock on a file next to the config, so a second
// instance working on the same config refuses to start (R-3, A07). The lock
// lasts while this process keeps the file open and goes with the process,
// however it ends: a killed run's lock frees itself, and nothing on disk has to
// be judged stale. The pid written into the file only names the owner in a
// refusal (11a).
//
// The file is never removed, not by cleanup either: an instance that opened it
// just before the unlink would lock the removed file while the next run created
// and locked a new one — two owners of one config.
func (a *App) acquireLock() error {
	f, err := openLockFile(a.lockFile)
	if err != nil {
		return fmt.Errorf("открытие lock-файла: %w", err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return fmt.Errorf("lock-файл %s занят — уже запущен другой экземпляр%s", a.lockFile, lockOwner(a.lockFile))
		}
		return fmt.Errorf("захват lock-файла %s: %w", a.lockFile, err)
	}
	// Under sudo root creates the file, and it outlives the run: root-owned
	// 0600, it would keep the next run without sudo from opening it. By
	// descriptor, so the handback cannot be redirected; best-effort, as for the
	// log.
	if err := restoreLockOwner(f); err != nil {
		slog.Warn("could not hand the lock file back to the sudo user", "file", a.lockFile, "error", err)
	}
	if err := writeLockPID(f); err != nil {
		_ = unlock(f)
		_ = f.Close()
		return fmt.Errorf("запись pid в lock-файл %s: %w", a.lockFile, err)
	}
	a.lock = f
	return nil
}

// writeLockPID puts our pid into the lock file in place of the last owner's; a
// var so a test can fail it.
var writeLockPID = func(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

// hasIPv6Stack decides whether a tun session gives its interface a v6 address
// and claims IPv6; a var so tests of both answers need no such kernel.
var hasIPv6Stack = system.HasIPv6Stack

// restoreLockOwner hands the lock file back to the sudo user; a var so a test
// can see it called without being root.
var restoreLockOwner = system.RestoreSudoOwnerFile

// lockOwner names the lock's owner in a refusal: " (pid N)", or nothing when the
// file names no running process — the owner writes its pid only after it has
// taken the lock, so for a moment the file still holds the previous one.
func lockOwner(path string) string {
	if pid, ok := readLockPID(path); ok && processAlive(pid) {
		return fmt.Sprintf(" (pid %d)", pid)
	}
	return ""
}

// readLockPID reads the owner pid from an existing lock file. A missing, empty
// or unparseable file returns ok=false.
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
