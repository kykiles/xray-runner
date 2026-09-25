package app

// The interface's side of the service (H10): a tun session run by the service,
// and the Linux split's cgroup and nft rules, with everything else — the menus,
// subscriptions, the proxy mode, the system proxy — staying in this process.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"xray-runner/internal/ipc"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// serviceConn is the connection to the service; an interface so tests can
// stand one in without a service.
type serviceConn interface {
	Call(ctx context.Context, typ string, req, reply any) error
	Events() <-chan ipc.Event
	Done() <-chan struct{}
	Close() error
	Hello() ipc.HelloReply
}

// NoServiceEnv, set to 1, keeps the interface in its own process even with a
// service to talk to.
const NoServiceEnv = "XRAY_RUNNER_NO_SERVICE"

// dialService reaches the service; a variable so tests need none.
var dialService = func(ctx context.Context, resume string) (serviceConn, error) {
	c, err := ipc.Connect(ctx, resume)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// remoteState is what the event reader and a remote session share.
type remoteState struct {
	mu    sync.Mutex
	conn  serviceConn
	token string
	// ended receives why the service ended the session on its own.
	ended chan error
	// unclosed are the split's moved processes, pid → name, whose
	// connections from before the move could not be closed; they stay listed
	// until the process exits, as system's own bookkeeping keeps them (14b).
	unclosed map[string]string
	// split is set while the Linux split goes through the service, and local
	// holds the split's own functions for when the service is gone.
	split bool
	local splitFuncs
}

// splitFuncs are the split's system side, as App holds them.
type splitFuncs struct {
	enable  func(names []string, tcpPort, dnsPort int) (system.SplitScan, error)
	rescan  func(names []string) (system.SplitScan, error)
	disable func() error
}

// serviceHelloTimeout is how long a dial waits for the service's hello. A
// service started by its socket on the first call runs the core's version and
// the journal's recovery first, which on a slow machine takes seconds.
const serviceHelloTimeout = 10 * time.Second

// useService connects to the service when there is one. Without it — none
// installed, stopped, or not this user's to use — the interface does it all in
// its own process, as before the service existed: the service is a way to run
// TUN without elevation, not a condition for running at all.
func (a *App) useService(ctx context.Context) {
	if os.Getenv(NoServiceEnv) == "1" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, serviceHelloTimeout)
	defer cancel()
	c, err := dialService(cctx, "")
	if err != nil {
		slog.Info("служба не используется", "reason", err)
		var re *ipc.RemoteError
		if errors.As(err, &re) || errors.Is(err, os.ErrPermission) {
			// There is a service, and it said no: the user should hear why.
			a.noteOnStatus("Служба xray-runner недоступна: " + err.Error())
		}
		return
	}
	h := c.Hello()
	slog.Info("работаем через службу", "service", h.ServiceVersion, "core", h.CoreVersion)
	a.attachService(c)
	if !system.SplitOverTUN {
		a.remote.mu.Lock()
		a.remote.split = true
		a.remote.local = splitFuncs{a.enableSplit, a.rescanSplit, a.disableSplit}
		a.remote.mu.Unlock()
		a.enableSplit = a.serviceEnableSplit
		a.rescanSplit = a.serviceRefreshSplit
		a.disableSplit = a.serviceDisableSplit
	}
}

// redialService, run before each session, replaces a connection that went
// while nothing was running: the service restarted or was updated under the
// open interface. A service that is gone for good leaves the interface on its
// own, as useService would have found it; the error says so.
func (a *App) redialService(ctx context.Context) error {
	c := a.service()
	if c == nil {
		return nil
	}
	select {
	case <-c.Done():
	default:
		return nil
	}
	slog.Warn("соединение со службой потеряно, подключаюсь заново")
	cctx, cancel := context.WithTimeout(ctx, serviceHelloTimeout)
	defer cancel()
	nc, err := dialService(cctx, "")
	if err != nil {
		a.dropService()
		return fmt.Errorf("служба xray-runner больше недоступна: %w", err)
	}
	h := nc.Hello()
	slog.Info("снова работаем через службу", "service", h.ServiceVersion, "core", h.CoreVersion)
	a.attachService(nc)
	return nil
}

// dropService forgets a service that is gone and gives the split back its
// own functions.
func (a *App) dropService() {
	a.remote.mu.Lock()
	c := a.remote.conn
	a.remote.conn = nil
	a.remote.token = ""
	split, local := a.remote.split, a.remote.local
	a.remote.split = false
	a.remote.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	if split {
		a.enableSplit, a.rescanSplit, a.disableSplit = local.enable, local.rescan, local.disable
	}
}

// attachService makes c the connection in use and starts reading its events.
func (a *App) attachService(c serviceConn) {
	a.remote.mu.Lock()
	a.remote.conn = c
	a.remote.mu.Unlock()
	go a.serviceEvents(c)
}

// service is the connection in use, nil without one.
func (a *App) service() serviceConn {
	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()
	return a.remote.conn
}

// closeService drops the connection at exit.
func (a *App) closeService() {
	a.remote.mu.Lock()
	c := a.remote.conn
	a.remote.conn = nil
	a.remote.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// serviceEvents reads what the service says until the connection goes.
func (a *App) serviceEvents(c serviceConn) {
	for ev := range c.Events() {
		switch ev.Kind {
		case ipc.EventLog:
			var lvl slog.Level
			if lvl.UnmarshalText([]byte(ev.Level)) != nil {
				lvl = slog.LevelInfo
			}
			slog.Log(context.Background(), lvl, "служба: "+ev.Line)
		case ipc.EventStatus:
			a.applyServiceStatus(ev)
		case ipc.EventEnded:
			a.remote.mu.Lock()
			ch := a.remote.ended
			a.remote.mu.Unlock()
			if ch != nil {
				err := errors.New("служба завершила сессию")
				if ev.Error != "" {
					err = errors.New(ev.Error)
				}
				select {
				case ch <- err:
				default:
				}
			}
		}
	}
}

// applyServiceStatus puts a status update from the service on the screen, the
// way the session's own loops would have.
func (a *App) applyServiceStatus(ev ipc.Event) {
	u := tui.StatusUpdate{
		OK: ev.OK, Latency: time.Duration(ev.LatencyMs) * time.Millisecond,
		Note: ev.Note, Err: ev.Err, ResetHealth: ev.ResetHealth, Generation: ev.Generation,
	}
	switch {
	case u.ResetHealth:
		a.resetHealth(u.Generation)
	case u.Note == "":
		a.statusMu.Lock()
		a.healthGen = u.Generation
		a.lastCheck = time.Now()
		a.lastCheckOK = u.OK
		a.sendStatusLocked(u)
		a.statusMu.Unlock()
	default:
		a.publishStatus(u)
	}
}

// serviceCoreReady is tunReady with the service in place of elevation: the
// service has the right, and its own core must be new enough.
func (a *App) serviceCoreReady(c serviceConn) error {
	v := c.Hello().CoreVersion
	if v != "" && !xray.MeetsMinVersion(v) {
		return fmt.Errorf("у службы xray-core %s, для TUN нужен %s или новее — обновите службу", parseCoreVersion(v), xray.MinVersion)
	}
	return nil
}

// runRemoteSession is runSession for a session built on the tunnel when the
// service runs it: the config is built here as for any session, and the
// service takes what a request may carry out of it (xraycfg.ClientParts).
func (a *App) runRemoteSession(ctx context.Context, t *target, split bool) (tui.StatusAction, error) {
	raw, ports, err := a.buildModeConfig(t)
	if err != nil {
		return tui.StatusQuit, err
	}
	parts, err := xraycfg.ExtractClientParts(raw)
	if err != nil {
		return tui.StatusQuit, err
	}
	a.noteGeoDatabases()
	a.hasBypass = xraycfg.HasBypassRules(raw)
	slog.Info("──── connecting via service: "+t.title()+" ────", "mode", a.mode)

	req := ipc.StartTun{
		Parts:         parts,
		LogLevel:      a.cfg.XrayLogLvl,
		AllowInsecure: a.cfg.AllowInsecure,
		KillSwitch:    a.cfg.KillSwitch,
		Split:         split,
		CheckURLs:     a.cfg.CheckURLs(),
		Title:         t.title(),
	}

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()
	ended := make(chan error, 1)
	a.remote.mu.Lock()
	a.remote.ended = ended
	a.remote.mu.Unlock()
	defer func() {
		a.remote.mu.Lock()
		a.remote.ended = nil
		a.remote.mu.Unlock()
	}()

	var endMu sync.Mutex
	var endErr error
	watchDone := make(chan struct{})
	teardown := func() {
		sessCancel()
		<-watchDone
		a.stopRemote()
		a.releaseSession()
	}

	connect := func() error {
		var rep ipc.StartReply
		if err := a.service().Call(sessCtx, ipc.TypeStartTun, req, &rep); err != nil {
			return err
		}
		a.remote.mu.Lock()
		a.remote.token = rep.Token
		a.remote.mu.Unlock()
		return nil
	}
	// The session ends when the service says so, or when the service is gone
	// for good.
	go func() {
		defer close(watchDone)
		err := a.watchRemote(sessCtx, ended)
		if err != nil {
			endMu.Lock()
			endErr = err
			endMu.Unlock()
			sessCancel()
		}
	}()
	remoteErr := func() error {
		endMu.Lock()
		defer endMu.Unlock()
		return endErr
	}

	if err := a.showConnecting(t, connect, sessCancel); err != nil {
		teardown()
		if e := remoteErr(); e != nil {
			return tui.StatusQuit, e
		}
		if errors.Is(err, tui.ErrConnectCanceled) {
			return tui.StatusQuit, ErrUserQuit
		}
		return tui.StatusQuit, fmt.Errorf("служба: %w", err)
	}

	action, err := a.watchSession(sessCtx, t, ports, func() {})
	teardown()
	if ctx.Err() != nil {
		return tui.StatusQuit, ctx.Err()
	}
	if e := remoteErr(); e != nil {
		return tui.StatusQuit, e
	}
	return action, err
}

// watchRemote waits for the session to end on the service's side. A
// connection that drops is dialled again and the session asked back
// (ipc.Hello.Resume): the service keeps it for a grace period.
func (a *App) watchRemote(ctx context.Context, ended <-chan error) error {
	for {
		c := a.service()
		if c == nil {
			return errors.New("служба недоступна")
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-ended:
			return err
		case <-c.Done():
		}
		slog.Warn("соединение со службой потеряно, подключаюсь заново")
		if err := a.reconnectService(ctx); err != nil {
			return err
		}
	}
}

// reconnectService dials the service again and asks for the session back.
func (a *App) reconnectService(ctx context.Context) error {
	a.remote.mu.Lock()
	token := a.remote.token
	a.remote.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		c, err := dialService(cctx, token)
		cancel()
		if err == nil {
			a.attachService(c)
			if token != "" && !c.Hello().Resumed {
				return errors.New("соединение со службой потеряно, и сессия не вернулась")
			}
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("соединение со службой потеряно: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// stopRemote asks the service to take the session down and waits until it
// has: the routes and the kill switch are gone once it returns.
func (a *App) stopRemote() {
	c := a.service()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.Call(ctx, ipc.TypeStop, struct{}{}, nil); err != nil {
		slog.Warn("служба не остановила сессию", "error", err)
	}
	a.remote.mu.Lock()
	a.remote.token = ""
	a.remote.mu.Unlock()
}

// The Linux split through the service: the same calls as system's, the
// processes moved by the service and only this user's.

// splitConnLost is closed once the connection the split went through is
// gone; nil while there is no split through the service to lose.
func (a *App) splitConnLost() <-chan struct{} {
	if !a.splitState() {
		return nil
	}
	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()
	if !a.remote.split || a.remote.conn == nil {
		return nil
	}
	return a.remote.conn.Done()
}

// resumeSplit is watchRemote for the split: the service takes the split down
// once its grace period is out, so the connection is dialled again at once
// and the split asked back. One that does not come back is switched off on
// the screen — the listed apps go direct from then on, and the user has to
// hear it.
func (a *App) resumeSplit(ctx context.Context) {
	slog.Warn("соединение со службой потеряно, возвращаю маршрутизацию по процессам")
	err := a.reconnectService(ctx)
	if ctx.Err() != nil {
		return
	}
	if err == nil && !a.service().Hello().Resumed {
		err = errors.New("соединение со службой потеряно, и маршрутизация по процессам не вернулась")
	}
	if err == nil {
		slog.Info("маршрутизация по процессам возвращена службой")
		return
	}
	slog.Error("split tunnel lost", "error", err)
	a.remote.mu.Lock()
	a.remote.token = ""
	a.remote.unclosed = nil
	a.remote.mu.Unlock()
	// Nothing left to undo: the service took the rules down with the session.
	a.setSplitMatched(false, nil)
	a.publishStatus(tui.StatusUpdate{
		Note: "Маршрутизация по процессам выключена: " + err.Error() + ". Программы из списка идут напрямую — подключитесь заново.",
		Err:  true, Apps: []string{},
	})
}

func (a *App) serviceEnableSplit(names []string, _, _ int) (system.SplitScan, error) {
	return a.serviceSplit(ipc.TypeEnableSplit, names)
}

func (a *App) serviceRefreshSplit(names []string) (system.SplitScan, error) {
	return a.serviceSplit(ipc.TypeRefreshSplit, names)
}

func (a *App) serviceSplit(typ string, names []string) (system.SplitScan, error) {
	c := a.service()
	if c == nil {
		return system.SplitScan{}, errors.New("служба недоступна")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var r ipc.SplitReply
	if err := c.Call(ctx, typ, ipc.SplitRequest{Names: names}, &r); err != nil {
		return system.SplitScan{}, err
	}
	failed := a.closeMovedConns(ctx, c, r.Moved)

	a.remote.mu.Lock()
	defer a.remote.mu.Unlock()
	if r.Token != "" {
		a.remote.token = r.Token
	}
	if a.remote.unclosed == nil {
		a.remote.unclosed = map[string]string{}
	}
	for pid, name := range failed {
		a.remote.unclosed[pid] = name
	}
	unclosed := map[string]bool{}
	for _, n := range r.Unclosed {
		unclosed[n] = true
	}
	for pid, name := range a.remote.unclosed {
		if n, err := strconv.Atoi(pid); err != nil || !processAlive(n) {
			// The process exited, and its sockets with it.
			delete(a.remote.unclosed, pid)
			continue
		}
		unclosed[name] = true
	}
	return system.SplitScan{Matched: r.Matched, Unclosed: slices.Sorted(maps.Keys(unclosed))}, nil
}

// closeMovedConns closes the connections the processes just moved into the
// split had open from before: those go past the tunnel, which only a close
// cures. This process lists them — they are its user's own sockets, which
// the service may not map to processes — and the service closes each after
// checking it is this user's. It returns the processes left with one open.
func (a *App) closeMovedConns(ctx context.Context, c serviceConn, moved map[string]string) map[string]string {
	if len(moved) == 0 {
		return nil
	}
	pids := map[string]bool{}
	for pid := range moved {
		pids[pid] = true
	}
	failed := map[string]string{}
	conns, err := connsOf(pids)
	if err != nil {
		slog.Warn("split: old connections not listed, they bypass the tunnel", "error", err)
		return moved
	}
	if len(conns) == 0 {
		return nil
	}
	req := ipc.CloseConns{}
	for conn := range conns {
		req.Conns = append(req.Conns, conn)
	}
	var rep ipc.CloseConnsReply
	if err := c.Call(ctx, ipc.TypeCloseConns, req, &rep); err != nil {
		slog.Warn("split: old connections not closed, they bypass the tunnel", "error", err)
		return moved
	}
	for _, conn := range rep.Open {
		for _, pid := range conns[conn] {
			failed[pid] = moved[pid]
		}
	}
	return failed
}

// connsOf lists the processes' connections; a variable so tests need no ss.
var connsOf = system.ConnsOf

func (a *App) serviceDisableSplit() error {
	a.remote.mu.Lock()
	a.remote.unclosed = nil
	a.remote.mu.Unlock()
	c := a.service()
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Call(ctx, ipc.TypeDisableSplit, struct{}{}, nil)
}
