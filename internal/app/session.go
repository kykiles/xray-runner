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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/config"
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
	appsPath := config.Path(system.AppsFile)
	apps, err := system.LoadApps(appsPath)
	if err != nil {
		slog.Warn("apps list not read", "file", appsPath, "error", err)
	}
	a.splitApps = apps
	a.resolveSplit()
	// Fixed for the session's life: the m key sets a.mode for the next session
	// while this one is still up, and a core restarted in between is still this
	// session's (C01).
	tun, split := a.tunMode(), a.split

	cfgJSON, ports, err := a.buildSessionConfig(t)
	if err != nil {
		return tui.StatusQuit, err
	}
	// R-3: our own previous session is already stopped here, so anything still
	// listening is a foreign process.
	if !a.tunMode() {
		for _, p := range []int{ports.socks, ports.http} {
			if portInUse(p) {
				return tui.StatusQuit, fmt.Errorf("порт %d уже занят другим процессом — возможно, запущен второй экземпляр", p)
			}
		}
	}
	// A11: before our core listens, a system proxy on a dead local port can only
	// be a leftover — say so before the session snapshots it as the original.
	a.warnDeadLoopbackProxy()
	// E03: the rules were checked against databases that are not the panel's
	// current ones — the screen says why.
	a.noteGeoDatabases()
	if err := a.writeConfigJSON(cfgJSON); err != nil {
		return tui.StatusQuit, err
	}
	// Read off the JSON that actually went to the core, so the label reflects
	// the panel's rules and ours alike.
	a.hasBypass = xraycfg.HasBypassRules(cfgJSON)

	a.setEndpoint(t)
	// Bracketed so the boundary between two servers is findable by eye: one run
	// of the tool logs every reconnect into the same file, and xray's own output
	// in between looks the same for either server.
	slog.Info("──── connecting: "+t.title()+" ────", "binary", a.binary, "mode", a.mode)

	a.runner = xray.New(a.binary, a.tmpFile)

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()

	var xrayDone sync.WaitGroup
	// coreErr is why the session ended on its own: the core died (A09), or a
	// restarted core could not be set up (C01). Written before sessCancel and
	// read only after teardown's xrayDone.Wait, which orders the two.
	var coreErr error

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
			return fmt.Errorf("проверка конфигурации xray не прошла: %w", explainInsecureRefusal(err))
		}
		// A tun session is set up around every core, not once: each core brings
		// up its own interface, and the routes on the last one went with it (C01).
		var gens *tunLifecycle
		var hooks xray.RunHooks
		if tun {
			gens = a.newTunLifecycle(t, ports, split)
			hooks = xray.RunHooks{AfterStart: gens.afterStart, AfterStop: gens.afterStop}
		}
		xrayDone.Add(1)
		go func() {
			defer xrayDone.Done()
			err := a.runner.RunWithRetryHooks(sessCtx, 5, hooks)
			if err == nil || errors.Is(err, context.Canceled) {
				return
			}
			slog.Error("xray runner failed", "error", err)
			// The session is already ending, for its own reason; a cleanup that
			// failed on the way out is tried again by releaseSession.
			if sessCtx.Err() != nil {
				return
			}
			coreErr = fmt.Errorf("ядро завершилось: %w", err)
			if gens != nil && gens.failed != nil {
				coreErr = gens.failed // the core was up; setting it up failed
			}
			sessCancel()
		}()
		if !tun {
			return a.bringUp(sessCtx, ports)
		}
		// Bring-up is done once the first core is set up — or once the session
		// ends, since a core that fails to start never gets that far.
		select {
		case err := <-gens.ready:
			return err
		case <-sessCtx.Done():
			return sessCtx.Err()
		}
	}

	if err := a.showConnecting(t, connect, sessCancel); err != nil {
		teardown()
		// A core that died mid bring-up is the cause; the port or interface wait
		// that then failed is only its symptom.
		if coreErr != nil {
			return tui.StatusQuit, coreErr
		}
		// M-2: an interrupted bring-up is a deliberate exit, not a failure — the
		// teardown above already undid the half-built session, and Ctrl+C means
		// "quit the app" on every other screen.
		if errors.Is(err, tui.ErrConnectCanceled) {
			return tui.StatusQuit, ErrUserQuit
		}
		return tui.StatusQuit, err
	}

	stopHealth = a.startHealth(sessCtx, ports, tun)
	action, err := a.watchSession(sessCtx, t, ports, stopHealth)
	teardown()
	if ctx.Err() != nil {
		return tui.StatusQuit, ctx.Err()
	}
	// The screen closed because the core died, not because the user left: an
	// error takes the menu back with the reason instead of quitting the app.
	if coreErr != nil {
		return tui.StatusQuit, coreErr
	}
	return action, err
}

// startHealth runs the session's background loops and returns a function that
// stops them and waits for them to finish. The waiting is the point: the loop
// reads a.runner while teardown nils it, and a loop outliving its session can
// ask the *next* session's core to restart. Calling the returned function more
// than once is safe — both the teardown and the screen-closer do.
//
// A tun session gets no health loop here: its health belongs to each core, and
// tunLifecycle runs one per core (C01).
func (a *App) startHealth(ctx context.Context, ports sessionPorts, tun bool) func() {
	hctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.splitRescanLoop(hctx)
	}()
	if !tun {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.healthLoop != nil {
				a.healthLoop(hctx, ports)
				return
			}
			a.healthCheckLoop(hctx, ports.http, 0, proxyCheckInterval)
		}()
	}
	return func() {
		cancel()
		wg.Wait()
	}
}

// startTunHealth runs the health loop of core gen and returns a function that
// stops it and waits for it to finish (C01). The core's results reach the
// screen from now until its reset.
func (a *App) startTunHealth(ctx context.Context, ports sessionPorts, gen int) func() {
	a.statusMu.Lock()
	a.healthGen = gen
	a.statusMu.Unlock()

	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if a.healthLoop != nil {
			a.healthLoop(hctx, ports)
			return
		}
		a.healthCheckLoop(hctx, ports.probe, gen, tunCheckInterval)
	}()
	return func() {
		cancel()
		<-done
	}
}

// watchSession shows the status screen. The update channel is closed under the
// lock publishers send under, so no publisher can send on a closed channel.
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
		note = tui.StatusUpdate{Note: switchErr.Error(), Err: true}
	}
}

// showStatusScreen draws one status screen with its own update channel. The
// channel is closed when the session context ends — a dead core takes the screen
// down with it instead of leaving the user staring at a dead session — under the
// lock publishers send under, so no publisher can send on a closed channel.
func (a *App) showStatusScreen(ctx context.Context, t *target, ports sessionPorts, stopHealth func(), note tui.StatusUpdate) (tui.StatusAction, error) {
	ch := make(chan tui.StatusUpdate, 8)
	a.setStatusCh(ch)

	if note.Note != "" {
		ch <- note // buffered, so this cannot block
	}

	go func() {
		<-ctx.Done()
		stopHealth()
		a.closeStatusCh(ch)
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

// closeStatusCh detaches and closes the given channel under the same lock
// publishStatus sends under. stopHealth does not cover every publisher — the
// bring-up goroutine (verifySystemProxy, the proxy test) is nobody's to wait on
// — so the lock, not the waiting, is what keeps a send off a closed channel.
func (a *App) closeStatusCh(ch chan tui.StatusUpdate) {
	a.statusMu.Lock()
	if a.statusCh == ch {
		a.statusCh = nil
	}
	close(ch)
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

// switchMode flips proxy⇄tun for the next session. TUN needs privileges and a
// recent core, so the check happens before the current session is torn down.
func (a *App) switchMode() error {
	next := nextMode(a.mode)
	if next == "tun" {
		if err := a.tunReady(); err != nil {
			return err
		}
	}
	a.mode = next
	a.rememberMode()
	return nil
}

// nextMode is the cycle the m key walks: proxy ⇄ tun. Split is not on it — it
// is proxy mode with processes to route, not a mode of its own (resolveSplit).
func nextMode(mode string) string {
	if mode == "proxy" {
		return "tun"
	}
	return "proxy"
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
	// probe is the loopback inbound a tun session's probes go through (A10).
	probe int
}

// buildSessionConfig produces the xray config for the target in the active
// mode. In TUN mode it also binds the freedom outbounds to the physical path:
// the panel's routing sends plenty of traffic out `direct`, and unbound those
// packets fall back into the tunnel they left and loop.
func (a *App) buildSessionConfig(t *target) (json.RawMessage, sessionPorts, error) {
	raw, ports, err := a.buildModeConfig(t)
	if err != nil || !a.tunMode() {
		return raw, ports, err
	}
	bind, err := a.directBind()
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("определить прямой путь мимо туннеля: %w", err)
	}
	bound, err := xraycfg.BindDirectOutbounds(raw, bind)
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("привязать прямые outbound-ы: %w", err)
	}
	a.directBound = bind
	return bound, ports, nil
}

// buildModeConfig is the config a session with this target runs and its preview
// shows, finished once, here, whichever source it came from (finalizeConfig).
func (a *App) buildModeConfig(t *target) (json.RawMessage, sessionPorts, error) {
	raw, ports, err := a.buildModeSource(t)
	if err != nil {
		return nil, sessionPorts{}, err
	}
	if raw, err = finalizeConfig(raw, a.cfg.AllowInsecure); err != nil {
		return nil, sessionPorts{}, err
	}
	return raw, ports, nil
}

// finalizeConfig is the last step of every config a core is handed — a
// session's, its preview's and each ping's: the local TLS policy (A01), then the
// check that the geo lists its rules name are in the databases (E03). Both need
// the finished config: a profile or a preserved outbound brings tlsSettings of
// its own, and the rules come from the profile or the template plus the split
// and probe ones added on top.
func finalizeConfig(raw json.RawMessage, allowInsecure bool) (json.RawMessage, error) {
	raw, err := xraycfg.ApplySecurityPolicy(raw, allowInsecure)
	if err != nil {
		return nil, fmt.Errorf("политика TLS (ALLOW_INSECURE): %w", err)
	}
	if err := xraycfg.CheckGeoLists(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// explainInsecureRefusal says what to do about a core that refused a config
// over allowInsecure. xray 26.7.28 refuses the setting outright, so
// ALLOW_INSECURE=true meets it with a server that asks for it; the core's words
// name the replacement settings, but the tool does not fill them in — a
// certificate seen over an untrusted network is no pin.
func explainInsecureRefusal(err error) error {
	if err == nil || !strings.Contains(err.Error(), "allowInsecure") {
		return err
	}
	return fmt.Errorf("ядро не поддерживает allowInsecure (отключение проверки сертификата): нужен сервер с действительным сертификатом либо доверенный pin (pinnedPeerCertSha256), заданный вами; %w", err)
}

// buildModeSource picks the config source for the target: a profile as-is, a
// single server under its profile's routing, or template.json.
func (a *App) buildModeSource(t *target) (json.RawMessage, sessionPorts, error) {
	inbounds := a.template.Inbounds
	if a.tunMode() {
		// A10: the probe goes through a loopback inbound of its own rather than
		// the system routes, so it travels the server's outbound whatever the
		// routes and the profile's direct rules say.
		pp, err := freePortPair()
		if err != nil {
			return nil, sessionPorts{}, fmt.Errorf("найти порт для проверки связи: %w", err)
		}
		inbounds = []xraycfg.Inbound{xraycfg.BuildTUNInbound(), xraycfg.BuildProbeInbound(pp.http)}
	} else if a.split {
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
		if raw, err = a.withSplitRouting(raw); err != nil {
			return nil, sessionPorts{}, err
		}
		// The health check must travel the tunnel it reports on: the panel's own
		// rules send the check hosts out direct, which is how the screen showed
		// "ок" while nothing was going through the proxy (ADR-0002).
		raw, err = xraycfg.PrependProbeRule(raw, probeHosts(a.cfg.CheckURLs()))
		if err != nil {
			return nil, sessionPorts{}, err
		}
		ports, err := portsFromInbounds(inbounds, a.tunMode())
		return raw, ports, err
	}

	// A single server picked out of a panel profile still runs under the panel's
	// routing and dns: those rules (direct .ru, blocked hosts) are the point of
	// the subscription, and rebuilding from template.json would drop them.
	raw, usedProfile, err := a.singleServerFromProfile(t, inbounds)
	if err != nil {
		return nil, sessionPorts{}, err
	}
	if usedProfile {
		ports, err := portsFromInbounds(inbounds, a.tunMode())
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

	raw, err = json.Marshal(cfg)
	if err != nil {
		return nil, sessionPorts{}, fmt.Errorf("marshal config: %w", err)
	}
	if raw, err = a.withSplitRouting(raw); err != nil {
		return nil, sessionPorts{}, err
	}
	// The other two branches prepend it themselves. Here it matters in every
	// mode: the template's rules meet the check hosts in proxy mode (A06), in
	// split, where the probe leaves our own process, which is not in the list,
	// and in tun, where the probe inbound's traffic goes through them. Without
	// this rule the check could go direct and report the tunnel healthy while
	// nothing at all goes through it (ADR-0002, A10).
	if raw, err = xraycfg.PrependProbeRule(raw, probeHosts(a.cfg.CheckURLs())); err != nil {
		return nil, sessionPorts{}, err
	}
	ports, err := portsFromInbounds(inbounds, a.tunMode())
	return raw, ports, err
}

// withSplitRouting rewrites a finished config so that only the processes from
// apps.txt travel the tunnel and everything else goes out directly. A no-op
// outside split mode, and outside the platforms where the mode is implemented
// by xray's own process matching rather than by the firewall (ADR-0003).
func (a *App) withSplitRouting(raw json.RawMessage) (json.RawMessage, error) {
	if !a.split || !system.SplitOverTUN {
		return raw, nil
	}
	return xraycfg.ApplySplitRouting(raw, a.splitApps)
}

// singleServerFromProfile builds the session from the profile the chosen server
// came from, pinned to that one server. usedProfile is false when there is
// nothing to preserve — a URL-list subscription, a bare link, a profile with no
// routing of its own — leaving the caller on the template.json path. A profile
// that is there but cannot be applied is an error (E03): on template.json the
// server would run without the panel's rules, and nothing on screen would say so.
func (a *App) singleServerFromProfile(t *target, inbounds []xraycfg.Inbound) (raw json.RawMessage, usedProfile bool, err error) {
	if len(t.profileRaw) == 0 || len(t.entry.RawOutbound) == 0 {
		return nil, false, nil
	}
	tag := xraycfg.OutboundTag(t.entry.RawOutbound)

	raw, err = xraycfg.MergeProfileSingle(t.profileRaw, tag, inbounds, a.cfg.XrayLogLvl)
	if errors.Is(err, xraycfg.ErrNoPanelRouting) {
		return nil, false, nil
	}
	if err == nil {
		raw, err = a.withSplitRouting(raw)
	}
	if err == nil {
		raw, err = xraycfg.PrependProbeRule(raw, probeHosts(a.cfg.CheckURLs()))
	}
	if err != nil {
		return nil, false, fmt.Errorf("правила профиля не применить к серверу: %w", err)
	}
	return raw, true, nil
}

// portsFromInbounds finds the SOCKS/HTTP ports by protocol/tag rather than by
// position, so reordering inbounds cannot silently swap them (Q-2).
func portsFromInbounds(inbounds []xraycfg.Inbound, tun bool) (sessionPorts, error) {
	if tun {
		for _, in := range inbounds {
			if in.Tag == xraycfg.ProbeInboundTag {
				return sessionPorts{probe: in.Port}, nil
			}
		}
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

// bringUp waits for xray to listen and enables the system proxy. A tun session
// is set up around each core instead (tunLifecycle).
func (a *App) bringUp(ctx context.Context, ports sessionPorts) error {
	if a.cfg.KillSwitch {
		// Not a failure: proxy mode has no system-wide traffic for a kill switch
		// to guard. No screen is up yet, so the note waits in pendingNote.
		slog.Info("kill switch skipped", "reason", "mode=proxy")
		note := "Kill switch действует только в TUN: в режиме PROXY он не включён."
		if a.pendingNote != "" {
			note = a.pendingNote + " " + note
		}
		a.pendingNote = note
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

	// One snapshot governs both ends: Enable refuses to touch settings this
	// snapshot could not restore, and teardown restores from the very same one.
	a.originalProxy = system.ReadProxyState()
	if err := a.proxy.Enable(ports.http, a.originalProxy); err != nil {
		slog.Warn("failed to enable system proxy", "error", err)
		// The user has to hear this from the screen, not from the log: otherwise
		// they only see that sites do not go through the VPN and cannot tell why.
		a.pendingNote = "Системный прокси не включён: " + err.Error() + ". VPN работает, но браузер и программы идут напрямую."
		return nil
	}
	a.proxyTouched = true
	slog.Info("system proxy enabled", "port", ports.http)

	// Only whether the OS took the setting: whether anything answers through the
	// proxy is the health check's to say, on the status line (A06).
	go a.verifySystemProxy(ports.http)
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
	// Where xray matches the owning process itself, split is the routing config
	// plus the tunnel — there is no ruleset to install here (ADR-0003).
	if system.SplitOverTUN {
		return
	}
	// Called even with an empty list: that is how EnableSplit gets to sweep a
	// ruleset an earlier killed run left behind.
	scan, err := a.enableSplit(a.splitApps, xraycfg.RedirectPort, xraycfg.RedirectDNS)
	if len(a.splitApps) == 0 {
		return
	}
	if err != nil {
		slog.Warn("split tunnel not enabled", "error", err)
		if splitNeedsRoot(err) {
			a.splitOff("нужны права root — запустите через sudo")
		} else {
			a.splitOff(err.Error())
		}
		return
	}
	matched := scan.Matched
	a.setSplitMatched(true, matched)
	a.statusMu.Lock()
	a.splitUnclosed = scan.Unclosed
	a.statusMu.Unlock()
	if len(scan.Unclosed) > 0 {
		// No screen yet: the note waits in pendingNote.
		note := unclosedNote(scan.Unclosed)
		if a.pendingNote != "" {
			note = a.pendingNote + " " + note
		}
		a.pendingNote = note
	}

	if len(matched) == 0 {
		// Not a note: the rescan loop picks up apps started later, and a sticky
		// note would still claim "nothing running" after they joined.
		slog.Info("split tunnel: no listed process is running", "listed", len(a.splitApps))
		return
	}
	slog.Info("split tunnel enabled", "processes", matched)
}

// splitNeedsRoot recognises the "no privileges" failure — nft refusing the
// ruleset, or the cgroup directory refusing to be created — so the user gets the
// same advice TUN gives instead of a raw nftables dump. Classified by the error
// alone: a non-root run can still have split working via CAP_NET_ADMIN, and its
// other failures (nft missing) must keep their own message.
func splitNeedsRoot(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not permitted") || strings.Contains(msg, "permission denied")
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
	scan, err := a.rescanSplit(a.splitApps)
	if err != nil {
		slog.Debug("split tunnel rescan failed", "error", err)
		return
	}
	matched := scan.Matched
	// Non-nil even when empty: the screen reads a nil Apps as "no rescan in this
	// update", so a list that emptied out would keep showing the old names.
	if matched == nil {
		matched = []string{}
	}

	a.statusMu.Lock()
	changed := !slices.Equal(matched, a.splitMatched)
	a.splitMatched = matched
	// Only the apps not reported yet: one stays in Unclosed on every rescan
	// until it exits, and the note must not come back each second.
	fresh := slices.DeleteFunc(slices.Clone(scan.Unclosed), func(n string) bool {
		return slices.Contains(a.splitUnclosed, n)
	})
	a.splitUnclosed = scan.Unclosed
	a.statusMu.Unlock()
	if len(fresh) > 0 {
		a.publishStatus(tui.StatusUpdate{Note: unclosedNote(fresh), Err: true})
	}
	if !changed {
		return
	}
	slog.Info("split tunnel rescanned", "processes", matched)
	a.publishStatus(tui.StatusUpdate{Apps: matched})
}

// unclosedNote names the apps whose connections from before they joined the
// split could not be closed: those go past the VPN until the app reconnects, and
// a restart is the sure way to make it (14b). Why it failed is in the log.
func unclosedNote(names []string) string {
	if len(names) == 1 {
		return "Не удалось закрыть старые соединения " + names[0] + ": они идут мимо VPN, пока программа не переподключится — перезапустите её (подробности в логе)."
	}
	return "Не удалось закрыть старые соединения " + strings.Join(names, ", ") + ": они идут мимо VPN, пока программы не переподключатся — перезапустите их (подробности в логе)."
}

// tunLifecycle sets a tun session up around every core process (C01): each core
// brings up its own interface, and the routes on the previous one went with it.
// The hooks run on the runner's goroutine, which owns a.tunRouted and
// a.killSwitchOn until teardown has waited the runner out.
type tunLifecycle struct {
	a     *App
	t     *target
	split bool // the session's, fixed when it started
	ports sessionPorts
	// bound is where the config sends direct traffic past the tunnel; it was
	// built for the adapter that was physical at the time.
	bound xraycfg.DirectBind
	gen   int
	route *system.TunRouteConfig // resolved by the first setup that gets there
	// stopHealth stops the current core's health loop and waits it out; nil
	// while no core is being checked.
	stopHealth func()
	// ready hands the first finished setup's result to bring-up. Buffered and
	// sent to once, so a later core's setup never blocks on it.
	ready chan error
	sent  bool
	// failed is a setup that failed under a live core: why the session ends.
	failed error
}

func (a *App) newTunLifecycle(t *target, ports sessionPorts, split bool) *tunLifecycle {
	return &tunLifecycle{a: a, t: t, split: split, ports: ports, bound: a.directBound, ready: make(chan error, 1)}
}

// afterStart sets up the core that has just started. An error while the core is
// still up ends the session; one caused by the core dying under the setup is
// only a symptom, and the runner restarts the core as for any crash.
func (l *tunLifecycle) afterStart(ctx context.Context) error {
	l.gen++
	l.failed = nil
	err := l.setUp(ctx)
	if err != nil && ctx.Err() != nil {
		return err
	}
	if err != nil {
		if l.gen > 1 {
			err = fmt.Errorf("после перезапуска ядра: %w", err)
		}
		l.failed = err
	} else {
		// Checked from now on, not before: through a core whose routes are not
		// in yet a check passes for a tunnel no traffic goes through (C01).
		l.stopHealth = l.a.startTunHealth(ctx, l.ports, l.gen)
	}
	if !l.sent {
		l.sent = true
		l.ready <- err
	}
	return err
}

// setUp routes the current core's interface. The first core to get that far
// also brings up the kill switch, which then stays for the whole session.
func (l *tunLifecycle) setUp(ctx context.Context) error {
	a := l.a
	if l.gen > 1 {
		// A config bound to an adapter that is gone sends direct traffic nowhere;
		// only a new connection builds one for the new adapter.
		bind, err := a.directBind()
		if err != nil {
			return fmt.Errorf("определить прямой путь мимо туннеля: %w", err)
		}
		if bind != l.bound {
			return fmt.Errorf("физический адаптер сменился («%s» → «%s»), а конфиг привязан к прежнему — подключитесь заново", l.bound.Interface, bind.Interface)
		}
	}
	if !a.awaitTUNInterface(ctx, xraycfg.TunInterfaceName, 10*time.Second) {
		slog.Error("tun interface did not come up")
		return fmt.Errorf("TUN-интерфейс %s не поднялся за 10с", xraycfg.TunInterfaceName)
	}
	slog.Info("tun interface activated")

	// The interface being up means nothing on its own: xray creates it and
	// reads packets off it, but never touches the routing table, so until the
	// routes below exist the tunnel receives no traffic at all.
	// Every tun session claims IPv6 (A11): left alone, a v6-capable app on a
	// dual-stack network goes around the tunnel with its real address.
	if l.route == nil {
		routeCfg := system.TunRouteConfig{
			Iface:      xraycfg.TunInterfaceName,
			Addr:       xraycfg.TunAddr,
			ServerIPs:  resolveAllIPs(l.t.serverHosts()),
			Addr6:      xraycfg.TunAddr6,
			ServerIPs6: resolveAllIPs6(l.t.serverHosts()),
		}
		if len(routeCfg.ServerIPs) == 0 {
			return fmt.Errorf("не удалось определить IP VPN-сервера — без него маршрутизация TUN оставит машину без сети")
		}
		l.route = &routeCfg
	}
	if err := a.enableTunRouting(*l.route); err != nil {
		return fmt.Errorf("настроить маршрутизацию TUN: %w", err)
	}
	a.tunRouted = true
	if l.sent {
		return nil
	}
	return a.enableTunKillSwitch(l.t, l.split)
}

// afterStop takes the core's routes out before the next core starts, so the
// next one's physical path is found with none of ours in the table. The kill
// switch stays up: between cores it is what keeps the apps off the physical link.
func (l *tunLifecycle) afterStop() error {
	a := l.a
	// The core's health goes first, waited out: nothing it still has in flight
	// may colour the screen once the reset has cleared it.
	if l.stopHealth != nil {
		l.stopHealth()
		l.stopHealth = nil
	}
	a.resetHealth(l.gen + 1)
	if !a.tunRouted {
		return nil
	}
	if err := a.disableTunRouting(); err != nil {
		// Still marked routed: the session's final teardown tries again.
		return fmt.Errorf("снять маршруты TUN: %w", err)
	}
	a.tunRouted = false
	return nil
}

// enableTunKillSwitch is the session's part of the tun bring-up, done once with
// the first core's routes in.
func (a *App) enableTunKillSwitch(t *target, split bool) error {
	if a.cfg.KillSwitch && split {
		// Kill switch cuts everything that does not go through the tunnel, which
		// in split mode is most of the system working as intended (ADR-0003).
		slog.Info("kill switch skipped", "reason", "mode=split")
		a.publishStatus(tui.StatusUpdate{
			Note: "Kill switch не работает в режиме SPLIT: трафик мимо туннеля здесь — норма.",
		})
	} else if a.cfg.KillSwitch {
		// A kill switch that was asked for and is not there must not pass for a
		// connected session (A02): the error ends bring-up, and the session's
		// teardown takes the routes above back out.
		//
		// A balancer profile rotates across many servers, and the whitelist holds
		// all of them — a single-endpoint whitelist used to make this case unsafe,
		// which is why it was skipped before.
		endpoints := a.killSwitchEndpoints(t)
		if len(endpoints) == 0 {
			// An empty whitelist would cut xray's own uplink along with the leak.
			return fmt.Errorf("kill switch не включён: не удалось определить IP серверов")
		}
		if err := a.enableKillSwitch(system.KillSwitchConfig{Endpoints: endpoints}); err != nil {
			return fmt.Errorf("kill switch не включился: %w", err)
		}
		a.killSwitchOn = true
		slog.Info("kill switch enabled", "endpoints", len(endpoints))
	}
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
		if err := a.disableTunRouting(); err != nil {
			// Still marked routed, so the teardown at exit tries again.
			slog.Warn("failed to remove tun routes", "error", err)
		} else {
			a.tunRouted = false
		}
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
	a.healthGen = 0
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
		// The protocol row answers "what am I connected through", and it answered
		// with the balancing strategy — which is not a protocol. The strategy moved
		// to its own row and this one now names the pool's transport stack.
		info.Protocol = poolProtocol(p.Entries)
		info.Balancer = fmt.Sprintf("%s · %d %s", p.Mode(), len(p.Entries), plural(len(p.Entries)))
	} else {
		e := t.entry
		info.Endpoint = fmt.Sprintf("%s:%d", e.Address, e.Port)
		info.Protocol = entryProtocol(*e)
	}

	info.NextMode = strings.ToUpper(nextMode(a.mode))

	switch {
	case a.split && system.SplitOverTUN:
		// No "captured N of M" here: xray matches the process when the connection
		// is opened, so there is nothing standing to count (ADR-0003).
		// The DNS caveat is on the screen because it is the one place the mode
		// does not do what its name says: Windows resolves through svchost.exe,
		// so DNS cannot be told apart by process (ADR-0003).
		info.Mode = "SPLIT (только выбранные процессы · DNS системы через VPN)"
		info.Split = true
		info.Apps = a.splitApps
	case a.mode == "tun":
		if a.hasBypass {
			info.Mode = "TUN (есть исключения по профилю)"
		} else {
			info.Mode = "TUN (весь трафик через VPN)"
		}
	default:
		addr := "127.0.0.1:" + strconv.Itoa(ports.http)
		info.Mode = "PROXY (" + addr + ")"
		// proxyTouched, not cfg.ProxySystem: it reports what actually happened,
		// so a system proxy that failed to apply reads the same as one turned off.
		if !a.proxyTouched {
			info.Mode += " · системный прокси выключен"
		}
		// The name the screen shows while listed processes are actually captured.
		// The port is the redirect one, not the HTTP proxy: that is where nft
		// actually sends the listed processes.
		info.SplitMode = "SPLIT (127.0.0.1:" + strconv.Itoa(xraycfg.RedirectPort) + ")"
		if a.proxyTouched {
			info.SplitMode += " · выбранные процессы + системный прокси"
		} else {
			info.SplitMode += " · только выбранные процессы"
		}
		// splitState, not the apps list: a split that refused to start (no root)
		// must not draw a process row claiming nothing is running.
		info.Split = a.splitState()
		info.Apps = a.splitMatched
	}
	return info
}

// entryProtocol names one server's transport stack: "vless / tcp / reality".
func entryProtocol(e subscription.SubEntry) string {
	parts := []string{e.Protocol}
	if e.Network != "" {
		parts = append(parts, e.Network)
	}
	if e.Security != "" {
		parts = append(parts, e.Security)
	}
	return strings.Join(parts, " / ")
}

// poolProtocol names what the servers behind a balancer have in common. They
// are interchangeable endpoints and normally share a stack, so the pool's
// answer is the connection's answer. When they do not share one, no single
// answer is true — and saying so beats borrowing the first server's and
// presenting it as the one in use, which is the mistake this row is fixing.
// Xray picks an outbound per connection, so there is no "current server" to
// name: several are live at once.
func poolProtocol(entries []subscription.SubEntry) string {
	if len(entries) == 0 {
		return "—"
	}
	first := entryProtocol(entries[0])
	for _, e := range entries[1:] {
		if entryProtocol(e) != first {
			return "смешанные"
		}
	}
	return first
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
