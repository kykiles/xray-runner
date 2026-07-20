package app

// One connected session: build the config, start xray, wire proxy/TUN, show the
// status screen, then tear everything down. Run repeats this per user action.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// runSession connects and blocks on the status screen until the user acts or
// the core dies. It always leaves the system as it found it before returning.
func (a *App) runSession(ctx context.Context, t *target) (tui.StatusAction, error) {
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

	// teardown runs on every exit path, including a failed bring-up.
	teardown := func() {
		sessCancel()
		xrayDone.Wait()
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

	if err := a.showConnecting(t, connect); err != nil {
		teardown()
		return tui.StatusQuit, err
	}

	action, err := a.watchSession(sessCtx, t, ports)
	teardown()
	if ctx.Err() != nil {
		return tui.StatusQuit, ctx.Err()
	}
	return action, err
}

// watchSession starts the health loops and shows the status screen. The update
// channel is closed only after every publisher has stopped, so no health loop
// can send on a closed channel.
func (a *App) watchSession(ctx context.Context, t *target, ports sessionPorts) (tui.StatusAction, error) {
	ch := make(chan tui.StatusUpdate, 8)
	a.statusMu.Lock()
	a.statusCh = ch
	a.statusMu.Unlock()

	var health sync.WaitGroup
	health.Add(1)
	go func() {
		defer health.Done()
		if a.mode == "tun" {
			a.healthCheckLoopConnectivity(ctx)
		} else {
			a.healthCheckLoopPorts(ctx, ports.socks, ports.http)
		}
	}()

	// U-2/U-3: a scripted or systemd run has no screen to draw on — keep the
	// session up until the context ends, with progress going to the log.
	if a.headless() {
		slog.Info("connected", "target", t.title(), "mode", a.mode)
		<-ctx.Done()
		health.Wait()
		return tui.StatusQuit, nil
	}

	// A note left by bring-up (e.g. the tun→proxy fallback) has had no screen to
	// appear on until now. The channel is buffered, so this cannot block.
	if a.pendingNote != "" {
		ch <- tui.StatusUpdate{Note: a.pendingNote, Err: true}
		a.pendingNote = ""
	}

	// If the core dies (or Ctrl+C arrives), close the screen instead of leaving
	// the user staring at a dead session. The channel is closed only after every
	// publisher has stopped, so no health loop can send on a closed channel.
	go func() {
		<-ctx.Done()
		health.Wait()
		a.statusMu.Lock()
		a.statusCh = nil
		a.statusMu.Unlock()
		close(ch)
	}()

	action, err := tui.ShowStatus(a.statusInfo(t, ports), ch)

	// The screen is gone; stop the publishers and wait for the closer.
	a.statusMu.Lock()
	a.statusCh = nil
	a.statusMu.Unlock()

	if err != nil {
		return tui.StatusQuit, fmt.Errorf("TUI: %w", err)
	}

	if action == tui.StatusSwitchMode {
		if err := a.switchMode(); err != nil {
			// Keep the session alive and tell the user why nothing changed.
			slog.Warn("mode switch refused", "error", err)
			return a.reshowStatus(ctx, t, ports, err)
		}
	}
	return action, nil
}

// reshowStatus re-enters the status screen after a refused mode switch, so the
// running session survives the failed attempt (Гард: TUN needs root).
func (a *App) reshowStatus(ctx context.Context, t *target, ports sessionPorts, refusal error) (tui.StatusAction, error) {
	select {
	case <-ctx.Done():
		return tui.StatusQuit, nil
	default:
	}

	ch := make(chan tui.StatusUpdate, 8)
	a.statusMu.Lock()
	a.statusCh = ch
	a.statusMu.Unlock()

	go func() {
		ch <- tui.StatusUpdate{Note: "⚠ " + refusal.Error(), Err: true}
	}()

	action, err := tui.ShowStatus(a.statusInfo(t, ports), ch)
	a.statusMu.Lock()
	a.statusCh = nil
	a.statusMu.Unlock()
	if err != nil {
		return tui.StatusQuit, fmt.Errorf("TUI: %w", err)
	}
	if action == tui.StatusSwitchMode {
		if err := a.switchMode(); err != nil {
			return a.reshowStatus(ctx, t, ports, err)
		}
	}
	return action, nil
}

// showConnecting runs the bring-up behind the alt-screen "Connecting…" spinner,
// or straight through with no screen on a headless run (U-2/U-3).
func (a *App) showConnecting(t *target, connect func() error) error {
	if a.headless() {
		return connect()
	}
	return tui.ShowConnecting(t.title(), connect)
}

// headless reports whether the session runs without an interactive screen:
// scripted selection (U-2) or no terminal at all (U-3, e.g. systemd).
func (a *App) headless() bool {
	if a.opts.Server != "" || a.opts.UseLast || a.opts.NonInteractive {
		return true
	}
	return !term.IsTerminal(int(os.Stdin.Fd()))
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

	a.originalProxy = system.ReadProxyState()
	if err := a.proxy.Enable(ports.http); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
		return nil
	}
	a.proxyTouched = true
	slog.Info("system proxy enabled", "port", ports.http)

	go func() {
		testProxyConnection(ctx, ports.http)
		a.verifySystemProxy(ports.http)
	}()
	return nil
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

	if a.cfg.KillSwitch && t.isProfile() {
		// The kill switch whitelists exactly one server endpoint; a balancer
		// profile rotates across many, so enabling it here would blackhole the
		// VPN's own traffic and leave the machine with no network at all.
		slog.Warn("kill switch skipped: balancer profile has no single server endpoint")
		a.publishStatus(tui.StatusUpdate{
			Note: "⚠ Kill switch выключен: профиль с балансировщиком использует несколько серверов. Выберите конкретный сервер, чтобы включить его.",
			Err:  true,
		})
	} else if a.cfg.KillSwitch {
		ksCfg := system.KillSwitchConfig{
			Endpoints: a.killSwitchEndpoints(t),
			XrayPath:  a.binary,
		}
		if err := system.EnableKillSwitch(ksCfg); err != nil {
			slog.Warn("kill switch failed", "error", err)
			a.publishStatus(tui.StatusUpdate{Note: "⚠ Kill switch не включился: " + err.Error(), Err: true})
		} else {
			slog.Info("kill switch enabled")
		}
	}

	go testConnectivity(ctx)
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
	if a.mode == "tun" {
		_ = system.DisableKillSwitch()
	} else if a.proxyTouched {
		if err := a.proxy.Restore(a.originalProxy); err != nil {
			slog.Warn("failed to restore proxy", "error", err)
		}
		a.proxyTouched = false
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
		info.NextMode = "TUN"
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
