package app

// One connected session: build the config, start xray, wire proxy/TUN, show the
// status screen, then tear everything down. Run repeats this per user action.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// runSession connects and blocks on the status screen until the user acts or
// the core dies. It always leaves the system as it found it before returning.
func (a *App) runSession(ctx context.Context, t *target) (tui.StatusAction, error) {
	// Re-read per session, not once at startup: editing apps.txt (or picking
	// processes on the screen) then reconnecting is how the list is changed.
	// A broken file is not worth refusing a connection over.
	apps, err := system.LoadApps(system.AppsFile)
	if err != nil {
		slog.Warn("apps list not read", "file", system.AppsFile, "error", err)
	}
	a.splitApps = apps

	cfgJSON, ports, err := a.buildSessionConfig(t)
	if err != nil {
		return tui.StatusQuit, err
	}
	// R-3: our own previous session is already stopped here, so anything still
	// listening is a foreign process.
	if a.mode != "tun" {
		for _, p := range []int{ports.socks, ports.http} {
			if portInUse(p) {
				return tui.StatusQuit, fmt.Errorf("порт %d уже занят другим процессом — возможно, запущен второй экземпляр", p)
			}
		}
	}
	if err := a.writeConfigJSON(cfgJSON); err != nil {
		return tui.StatusQuit, err
	}

	a.setEndpoint(t)
	// Bracketed so the boundary between two servers is findable by eye: one run
	// of the tool logs every reconnect into the same file, and xray's own output
	// in between looks the same for either server.
	slog.Info("──── connecting: "+t.title()+" ────", "binary", a.binary, "mode", a.mode)

	a.runner = xray.New(a.binary, a.tmpFile)

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()

	var xrayDone sync.WaitGroup

	// Set once the health loops are up; teardown must wait for them before
	// releaseSession touches a.runner underneath them.
	stopHealth := func() {}

	// teardown runs on every exit path, including a failed bring-up.
	teardown := func() {
		sessCancel()
		xrayDone.Wait()
		stopHealth()
		a.releaseSession()
	}

	// connect validates the config, starts the core and brings up proxy/TUN. It
	// runs behind the "Connecting…" screen so the shell never flashes between the
	// server list and the status screen (task #2).
	connect := func() error {
		// X-1: validate the config up front; a test failure is a deterministic
		// error and must not be retried.
		if err := a.runner.TestConfig(sessCtx); err != nil {
			return fmt.Errorf("проверка конфигурации xray не прошла: %w", err)
		}
		xrayDone.Add(1)
		go func() {
			defer xrayDone.Done()
			if err := a.runner.RunWithRetry(sessCtx, 5); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("xray runner failed", "error", err)
				sessCancel()
			}
		}()
		return a.bringUp(sessCtx, t, ports)
	}

	if err := a.showConnecting(t, connect, sessCancel); err != nil {
		teardown()
		// M-2: an interrupted bring-up is a deliberate exit, not a failure — the
		// teardown above already undid the half-built session, and Ctrl+C means
		// "quit the app" on every other screen.
		if errors.Is(err, tui.ErrConnectCanceled) {
			return tui.StatusQuit, ErrUserQuit
		}
		return tui.StatusQuit, err
	}

	stopHealth = a.startHealth(sessCtx, ports)
	action, err := a.watchSession(sessCtx, t, ports, stopHealth)
	teardown()
	if ctx.Err() != nil {
		return tui.StatusQuit, ctx.Err()
	}
	return action, err
}

// startHealth runs the mode's health loop and returns a function that stops it
// and waits for it to finish. The waiting is the point: the loop reads a.runner
// while teardown nils it, and a loop outliving its session can ask the *next*
// session's core to restart. Calling the returned function more than once is
// safe — both the teardown and the screen-closer do.
func (a *App) startHealth(ctx context.Context, ports sessionPorts) func() {
	hctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.splitRescanLoop(hctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		switch {
		case a.healthLoop != nil:
			a.healthLoop(hctx, ports)
		case a.mode == "tun":
			a.healthCheckLoopConnectivity(hctx)
		default:
			a.healthCheckLoopPorts(hctx, ports.socks, ports.http)
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// watchSession shows the status screen. The update channel is closed only after
// every publisher has stopped, so no health loop can send on a closed channel.
func (a *App) watchSession(ctx context.Context, t *target, ports sessionPorts, stopHealth func()) (tui.StatusAction, error) {
	// U-2/U-3: a scripted or systemd run has no screen to draw on — keep the
	// session up until the context ends, with progress going to the log.
	if a.headless() {
		slog.Info("connected", "target", t.title(), "mode", a.mode)
		<-ctx.Done()
		stopHealth()
		return tui.StatusQuit, nil
	}

	// A note left by bring-up (e.g. the tun→proxy fallback) has had no screen to
	// appear on until now.
	note := tui.StatusUpdate{Note: a.pendingNote, Err: true}
	a.pendingNote = ""

	// A refused mode switch (TUN without root) must not end the session: the
	// screen comes back with the refusal on it, which is why this is a loop.
	for {
		action, err := a.showStatusScreen(ctx, t, ports, stopHealth, note)
		if err != nil {
			return tui.StatusQuit, err
		}
		if ctx.Err() != nil {
			return tui.StatusQuit, nil
		}
		if action != tui.StatusSwitchMode {
			return action, nil
		}
		switchErr := a.switchMode()
		if switchErr == nil {
			return action, nil
		}
		slog.Warn("mode switch refused", "error", switchErr)
		note = tui.StatusUpdate{Note: "⚠ " + switchErr.Error(), Err: true}
	}
}

// showStatusScreen draws one status screen with its own update channel. The
// channel is closed when the session context ends — a dead core takes the screen
// down with it instead of leaving the user staring at a dead session — and only
// after every publisher has stopped, so no health loop can send on a closed
// channel.
func (a *App) showStatusScreen(ctx context.Context, t *target, ports sessionPorts, stopHealth func(), note tui.StatusUpdate) (tui.StatusAction, error) {
	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)

	if note.Note != "" {
		ch <- note // buffered, so this cannot block
	}

	go func() {
		<-ctx.Done()
		stopHealth()
		a.clearStatusCh(ch)
		close(ch)
	}()

	action, err := a.showStatus(a.statusInfo(t, ports), ch)

	// The screen is gone; publishers must stop aiming at its channel.
	a.clearStatusCh(ch)

	if err != nil {
		return tui.StatusQuit, fmt.Errorf("TUI: %w", err)
	}
	return action, nil
}

func (a *App) setStatusCh(ch chan tui.StatusUpdate) {
	a.statusMu.Lock()
	a.statusCh = ch
	a.statusMu.Unlock()
}

// clearStatusCh detaches the given channel only if it is still the live one: a
// closer goroutine from a previous screen must not silence the current one.
func (a *App) clearStatusCh(ch chan tui.StatusUpdate) {
	a.statusMu.Lock()
	if a.statusCh == ch {
		a.statusCh = nil
	}
	a.statusMu.Unlock()
}

// showConnecting runs the bring-up behind the alt-screen "Connecting…" spinner,
// or straight through with no screen on a headless run (U-2/U-3).
// cancel aborts the bring-up when the user interrupts it; a headless run has no
// screen to interrupt from, so it just runs connect through.
func (a *App) showConnecting(t *target, connect func() error, cancel func()) error {
	if a.headless() {
		return connect()
	}
	return a.connectScreen(t.title(), connect, cancel)
}

// headless reports whether the session runs without an interactive screen:
// scripted selection (U-2) or no terminal at all (U-3, e.g. systemd).
func (a *App) headless() bool {
	return a.opts.Server != "" || a.opts.UseLast || a.opts.NonInteractive || a.noTTY
}

// switchMode flips proxy⇄tun for the next session. TUN needs privileges, so the
// check happens before the current session is torn down.
func (a *App) switchMode() error {
	if a.mode == "tun" {
		a.mode = "proxy"
		a.rememberMode()
		return nil
	}
	if err := a.checkPrivileges(); err != nil {
		return err
	}
	a.mode = "tun"
	a.rememberMode()
	return nil
}

// rememberMode persists the switch so the next start comes up the same way. A
// refused switch never reaches here: only a mode the user actually got is worth
// restoring.
func (a *App) rememberMode() {
	if err := updateState(func(s *LastState) { s.Mode = a.mode }); err != nil {
		slog.Warn("failed to save mode state", "error", err)
	}
}

// previewConfig renders the config a session with this target would launch,
// pretty-printed for the TUI viewer. It goes through the same builder as the
// session, so dns, routing rules and outbounds on screen are the ones that run —
// minus what this tool bolts on top: our inbounds (SOCKS/HTTP or the TUN
// interface), our log level, and the direct-path binding TUN mode adds. None of
// that comes from the panel, and showing it only obscures the real config.
func (a *App) previewConfig(t *target) (string, error) {
	raw, _, err := a.buildModeConfig(t) // not buildSessionConfig: skips the TUN sendThrough binding
	if err != nil {
		return "", err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	delete(cfg, "inbounds")
	delete(cfg, "log")
	pretty, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(pretty), nil
}

type sessionPorts struct {
	socks int
	http  int
}

// buildSessionConfig produces the xray config for the target in the active
// mode. In TUN mode it also binds the freedom outbounds to the physical path:
// the panel's routing sends plenty of traffic out `direct`, and unbound those
// packets fall back into the tunnel they left and loop.
func (a *App) buildSessionConfig(t *target) (json.RawMessage, sessionPorts, error) {
	raw, ports, err := a.buildModeConfig(t)
	if err != nil || a.mode != "tun" {
		return raw, ports, err
	}
	bind, err := system.DirectBind()
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("определить прямой путь мимо туннеля: %w", err)
	}
	bound, err := xraycfg.BindDirectOutbounds(raw, bind)
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("привязать прямые outbound-ы: %w", err)
	}
	return bound, ports, nil
}

// buildModeConfig picks the config source for the target: a profile as-is, a
// single server under its profile's routing, or template.json.
func (a *App) buildModeConfig(t *target) (json.RawMessage, sessionPorts, error) {
	inbounds := a.template.Inbounds
	if a.mode == "tun" {
		inbounds = []xraycfg.Inbound{xraycfg.BuildTUNInbound()}
	} else if len(a.splitApps) > 0 {
		// The listeners the split-tunnel nft rules redirect into. They are added,
		// not substituted: the SOCKS/HTTP pair still serves the system proxy, and
		// PROXY_SYSTEM decides whether anything is pointed at it.
		inbounds = append(append([]xraycfg.Inbound{}, inbounds...), xraycfg.BuildRedirectInbounds()...)
	}

	if t.isProfile() {
		// The profile brings its own outbounds/routing/balancer.
		raw, err := xraycfg.MergeProfile(t.profileRaw, inbounds, a.cfg.XrayLogLvl)
		if err != nil {
			return nil, sessionPorts{}, err
		}
		ports, err := portsFromInbounds(inbounds, a.mode)
		return raw, ports, err
	}

	// A single server picked out of a panel profile still runs under the panel's
	// routing and dns: those rules (direct .ru, blocked hosts) are the point of
	// the subscription, and rebuilding from template.json would drop them.
	if raw, ok := a.singleServerFromProfile(t, inbounds); ok {
		ports, err := portsFromInbounds(inbounds, a.mode)
		return raw, ports, err
	}

	outbound, err := subscription.ProxyOutboundJSON(t.entry)
	if err != nil {
		return nil, sessionPorts{}, err
	}
	tc := *a.template
	tc.Inbounds = inbounds
	cfg := xraycfg.MergeConfig(&tc, outbound)
	cfg.Log = &xraycfg.LogConfig{Loglevel: a.cfg.XrayLogLvl}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("marshal config: %w", err)
	}
	ports, err := portsFromInbounds(inbounds, a.mode)
	return raw, ports, err
}

// singleServerFromProfile builds the session from the profile the chosen server
// came from, pinned to that one server. It reports false when there is nothing
// to preserve — a URL-list subscription or a bare link — leaving the caller on
// the template.json path.
func (a *App) singleServerFromProfile(t *target, inbounds []xraycfg.Inbound) (json.RawMessage, bool) {
	if len(t.profileRaw) == 0 || len(t.entry.RawOutbound) == 0 {
		return nil, false
	}
	tag := xraycfg.OutboundTag(t.entry.RawOutbound)
	if tag == "" {
		return nil, false
	}

	raw, err := xraycfg.MergeProfileSingle(t.profileRaw, tag, inbounds, a.cfg.XrayLogLvl)
	if err != nil {
		// Degrade to template.json rather than refuse to connect: the session
		// still works, just without the panel's rules.
		if !errors.Is(err, xraycfg.ErrNoPanelRouting) {
			slog.Warn("panel routing not applied, falling back to template", "error", err)
		}
		return nil, false
	}
	return raw, true
}

// portsFromInbounds finds the SOCKS/HTTP ports by protocol/tag rather than by
// position, so reordering inbounds cannot silently swap them (Q-2).
func portsFromInbounds(inbounds []xraycfg.Inbound, mode string) (sessionPorts, error) {
	if mode == "tun" {
		return sessionPorts{}, nil
	}
	var p sessionPorts
	for _, in := range inbounds {
		switch {
		case in.Protocol == "socks" || in.Tag == "socks":
			p.socks = in.Port
		case in.Protocol == "http" || in.Tag == "http":
			p.http = in.Port
		}
	}
	if p.socks == 0 || p.http == 0 {
		return p, fmt.Errorf("в конфиге не найдены socks/http inbound-порты (socks=%d http=%d)", p.socks, p.http)
	}
	return p, nil
}

// bringUp waits for xray to listen and enables proxy/kill switch.
func (a *App) bringUp(ctx context.Context, t *target, ports sessionPorts) error {
	if a.mode == "tun" {
		return a.bringUpTun(ctx, t)
	}
	return a.bringUpProxy(ctx, ports)
}

func (a *App) bringUpProxy(ctx context.Context, ports sessionPorts) error {
	slog.Info("proxy listening", "socks5", ports.socks, "http", ports.http)

	if !awaitPort(ctx, ports.socks, "SOCKS5", 10*time.Second) {
		return fmt.Errorf("SOCKS5 порт не открылся за 10с")
	}
	if !awaitPort(ctx, ports.http, "HTTP", 5*time.Second) {
		return fmt.Errorf("HTTP порт не открылся за 5с")
	}

	a.bringUpSplit()

	// PROXY_SYSTEM=false is the "only the listed apps" case: the system proxy
	// stays untouched, so the browser and everything else keep going direct.
	if !a.cfg.ProxySystem {
		slog.Info("system proxy left alone", "reason", "PROXY_SYSTEM=false")
		return nil
	}

	a.originalProxy = system.ReadProxyState()
	if err := a.proxy.Enable(ports.http); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
		return nil
	}
	a.proxyTouched = true
	slog.Info("system proxy enabled", "port", ports.http)

	go func() {
		a.testProxyConnection(ctx, ports.http)
		a.verifySystemProxy(ports.http)
	}()
	return nil
}

// bringUpSplit routes the processes from apps.txt through the proxy. It never
// fails the session: split tunnelling is an addition to proxy mode, and losing
// it leaves a working proxy rather than no connection. What went wrong lands on
// the status screen instead.
//
// The list is re-scanned every second (splitRescanLoop), so an app started
// after connecting joins the tunnel on its own.
func (a *App) bringUpSplit() {
	// Called even with an empty list: that is how EnableSplit gets to sweep a
	// ruleset an earlier killed run left behind.
	matched, err := system.EnableSplit(a.splitApps, xraycfg.RedirectPort, xraycfg.RedirectDNS)
	if len(a.splitApps) == 0 {
		return
	}
	if err != nil {
		slog.Warn("split tunnel not enabled", "error", err)
		a.pendingNote = "Маршрутизация по процессам не включена: " + err.Error()
		return
	}
	a.setSplitMatched(true, matched)

	if len(matched) == 0 {
		slog.Info("split tunnel: no listed process is running", "listed", len(a.splitApps))
		a.pendingNote = "Сейчас нет запущенных процессов из " + system.AppsFile
		return
	}
	slog.Info("split tunnel enabled", "processes", matched)
}

// splitState / setSplitMatched guard the split fields: the health loop rescans
// from its own goroutine while teardown clears them from the session's.
func (a *App) splitState() bool {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.splitOn
}

func (a *App) setSplitMatched(on bool, matched []string) {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	a.splitOn, a.splitMatched = on, matched
}

// splitRescanLoop catches processes that start mid-session. It runs on its own
// short tick rather than the health one: a CLI like claude fires its first API
// request within a second of exec, and a request that leaves before the process
// joins the cgroup goes out untunnelled — which is the whole failure, since the
// server answers it with a hard error instead of a retry.
//
// ponytail: полный /proc-скан раз в секунду; окно остаётся ~1с. Если и его
// мало — netlink proc connector (PROC_EVENT_EXEC) даёт событие на exec.
func (a *App) splitRescanLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshSplit()
		}
	}
}

// refreshSplit moves processes that started after the session did into the
// cgroup. The nft rules match the cgroup rather than a PID, so joining it is all
// a late app needs — no reconnect, no rule rebuild.
func (a *App) refreshSplit() {
	if !a.splitState() {
		return
	}
	matched, err := system.RefreshSplit(a.splitApps)
	if err != nil {
		slog.Debug("split tunnel rescan failed", "error", err)
		return
	}

	a.statusMu.Lock()
	changed := !slices.Equal(matched, a.splitMatched)
	a.splitMatched = matched
	a.statusMu.Unlock()
	if !changed {
		return
	}
	slog.Info("split tunnel rescanned", "processes", matched)
	a.publishStatus(tui.StatusUpdate{Apps: matched})
}

func (a *App) bringUpTun(ctx context.Context, t *target) error {
	if !a.awaitTUNInterface(ctx, xraycfg.TunInterfaceName, 10*time.Second) {
		slog.Error("tun interface did not come up")
		return fmt.Errorf("TUN-интерфейс %s не поднялся за 10с", xraycfg.TunInterfaceName)
	}
	slog.Info("tun interface activated")

	// The interface being up means nothing on its own: xray creates it and
	// reads packets off it, but never touches the routing table, so until the
	// routes below exist the tunnel receives no traffic at all.
	routeCfg := system.TunRouteConfig{
		Iface:     xraycfg.TunInterfaceName,
		Addr:      xraycfg.TunAddr,
		ServerIPs: resolveAllIPs(t.serverHosts()),
	}
	if len(routeCfg.ServerIPs) == 0 {
		return fmt.Errorf("не удалось определить IP VPN-сервера — без него маршрутизация TUN оставит машину без сети")
	}
	if err := system.EnableTunRouting(routeCfg); err != nil {
		return fmt.Errorf("настроить маршрутизацию TUN: %w", err)
	}
	a.tunRouted = true

	if a.cfg.KillSwitch {
		// A balancer profile rotates across many servers, and the whitelist holds
		// all of them — a single-endpoint whitelist used to make this case unsafe,
		// which is why it was skipped before.
		endpoints := a.killSwitchEndpoints(t)
		if len(endpoints) == 0 {
			slog.Warn("kill switch skipped: no server endpoint resolved")
			a.publishStatus(tui.StatusUpdate{
				Note: "⚠ Kill switch выключен: не удалось определить IP серверов.",
				Err:  true,
			})
		} else if err := system.EnableKillSwitch(system.KillSwitchConfig{
			Endpoints: endpoints,
			XrayPath:  a.binary,
		}); err != nil {
			slog.Warn("kill switch failed", "error", err)
			a.publishStatus(tui.StatusUpdate{Note: "⚠ Kill switch не включился: " + err.Error(), Err: true})
		} else {
			a.killSwitchOn = true
			slog.Info("kill switch enabled", "endpoints", len(endpoints))
		}
	}

	go reachCheck(ctx, &http.Client{Timeout: 10 * time.Second}, a.cfg.CheckURLs(), "connectivity check")
	return nil
}

// killSwitchEndpoints lists every server the session's uplink may dial. The
// chosen server comes first, then the profile's siblings: a panel rule can send
// some domains through any outbound of the profile the session runs under, and
// a sibling missing from the whitelist is dropped with no diagnostic (M-1).
func (a *App) killSwitchEndpoints(t *target) []system.Endpoint {
	var out []system.Endpoint
	seen := map[system.Endpoint]bool{}
	add := func(host string, port int, udp bool) {
		for _, ip := range resolveAllIPs([]string{host}) {
			e := system.Endpoint{IP: ip, Port: port, UDP: udp}
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
	}

	add(a.serverHost, a.serverPort, a.serverUDP)
	for _, e := range t.profileSrvs {
		add(e.Address, e.Port, e.Protocol == "hysteria2")
	}
	return out
}

// releaseSession undoes everything bringUp did, leaving the OS clean for the
// next session (or for exit).
func (a *App) releaseSession() {
	// Routes first: they are what stands between the user and a working
	// network, so restore the physical path before anything else can fail.
	if a.tunRouted {
		if err := system.DisableTunRouting(); err != nil {
			slog.Warn("failed to remove tun routes", "error", err)
		}
		a.tunRouted = false
	}
	// Undo by what was actually enabled, never by a.mode: the m key switches the
	// mode while the session is still up, so by the time teardown runs a.mode
	// already names the *next* session's mode.
	if a.killSwitchOn {
		if err := a.disableKillSwitch(); err != nil {
			slog.Warn("failed to disable kill switch", "error", err)
		}
		a.killSwitchOn = false
	}
	if a.proxyTouched {
		if err := a.restoreProxy(a.originalProxy); err != nil {
			slog.Warn("failed to restore proxy", "error", err)
		}
		a.proxyTouched = false
	}
	if a.splitState() {
		if err := a.disableSplit(); err != nil {
			slog.Warn("failed to disable split tunnel", "error", err)
		}
		a.setSplitMatched(false, nil)
	}
	if a.runner != nil {
		_ = a.runner.Stop()
		a.runner = nil
	}
	a.statusMu.Lock()
	a.lastCheck = time.Time{}
	a.lastCheckOK = false
	a.statusMu.Unlock()
}

// setEndpoint records the selected server so the kill switch can allow xray's
// own traffic to it (H-1). A balancer profile has many servers, so no single
// endpoint is recorded.
func (a *App) setEndpoint(t *target) {
	if t.isProfile() {
		a.serverHost, a.serverPort, a.serverUDP = "", 0, false
		return
	}
	a.setServerEndpoint(t.entry)
	resolveServer(t.entry.Address)
}

func (a *App) statusInfo(t *target, ports sessionPorts) tui.StatusInfo {
	info := tui.StatusInfo{Title: t.title()}

	if t.isProfile() {
		p := a.nav.profiles[a.nav.profIdx]
		info.Protocol = fmt.Sprintf("%s · %d %s", p.Mode(), len(p.Entries), plural(len(p.Entries)))
	} else {
		e := t.entry
		info.Endpoint = fmt.Sprintf("%s:%d", e.Address, e.Port)
		parts := []string{e.Protocol}
		if e.Network != "" {
			parts = append(parts, e.Network)
		}
		if e.Security != "" {
			parts = append(parts, e.Security)
		}
		info.Protocol = strings.Join(parts, " / ")
	}

	if a.mode == "tun" {
		info.Mode = "TUN (весь трафик через VPN)"
		info.NextMode = "PROXY"
	} else {
		info.Mode = "PROXY (127.0.0.1:" + strconv.Itoa(ports.http) + ")"
		// proxyTouched, not cfg.ProxySystem: it reports what actually happened,
		// so a system proxy that failed to apply reads the same as one turned off.
		if !a.proxyTouched {
			info.Mode += " · системный прокси выключен"
		}
		info.NextMode = "TUN"
		info.Apps = a.splitMatched
	}
	return info
}

func plural(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return "сервер"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return "сервера"
	default:
		return "серверов"
	}
}
