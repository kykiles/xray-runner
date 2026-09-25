package app

// The interface's side of the service (H10): a tun session run by the service,
// and the Linux split's cgroup and nft rules, with everything else — the menus,
// subscriptions, the proxy mode, the system proxy — staying in this process.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
}

// useService connects to the service when there is one. Without it — none
// installed, stopped, or not this user's to use — the interface does it all in
// its own process, as before the service existed: the service is a way to run
// TUN without elevation, not a condition for running at all.
func (a *App) useService(ctx context.Context) {
	if os.Getenv(NoServiceEnv) == "1" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
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
		a.enableSplit = a.serviceEnableSplit
		a.rescanSplit = a.serviceRefreshSplit
		a.disableSplit = a.serviceDisableSplit
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
	a.remote.mu.Lock()
	if r.Token != "" {
		a.remote.token = r.Token
	}
	a.remote.mu.Unlock()
	return system.SplitScan{Matched: r.Matched, Unclosed: r.Unclosed}, nil
}

func (a *App) serviceDisableSplit() error {
	c := a.service()
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Call(ctx, ipc.TypeDisableSplit, struct{}{}, nil)
}
