package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// ServiceTun is a tun session as the service runs it for an interface (H10).
// The interface sends the structured parts of the config; the rest — the
// inbounds, the log, the core — is the service's own.
type ServiceTun struct {
	Parts         xraycfg.ClientParts
	LogLevel      string
	AllowInsecure bool
	KillSwitch    bool
	// Split routes only the processes named in the parts' routing (Windows,
	// where xray matches the process); no kill switch then.
	Split     bool
	CheckURLs []string
	Title     string
	// Binary is the service's own core.
	Binary string
}

// prepareServiceApp lets tests put fakes in place of the machine.
var prepareServiceApp = func(*App) {}

// ServeTun runs one tun session through the same bring-up and teardown as a
// session in the interface's own process — tunLifecycle, the kill switch,
// releaseSession — until ctx ends or the core gives up. ready is called once,
// with nil when the first core is set up or with the reason it was not;
// status gets what the session would show on its status screen; it must not
// block — ServeTun hands the last update over before it returns, and a status
// that waits for a slow reader would hold the session's end on it. The system
// is left as it was found on every way out.
func ServeTun(ctx context.Context, spec ServiceTun, ready func(error), status func(tui.StatusUpdate)) error {
	var once sync.Once
	signal := func(err error) { once.Do(func() { ready(err) }) }
	err := serveTun(ctx, spec, signal, status)
	signal(err)
	return err
}

func serveTun(ctx context.Context, spec ServiceTun, ready func(error), status func(tui.StatusUpdate)) error {
	if spec.Split && !system.SplitOverTUN {
		return errors.New("маршрутизация по процессам здесь не строится на туннеле")
	}
	cfg := &config.Config{
		Mode:            "tun",
		KillSwitch:      spec.KillSwitch,
		XrayLogLvl:      spec.LogLevel,
		AllowInsecure:   spec.AllowInsecure,
		HealthCheckURLs: spec.CheckURLs,
	}
	a := New(cfg, Options{NonInteractive: true})
	prepareServiceApp(a)
	a.noTTY = true
	a.binary = spec.Binary
	if spec.Split {
		a.mode, a.split = "proxy", true
	}

	pp, err := freePortPair()
	if err != nil {
		return fmt.Errorf("найти порт для проверки связи: %w", err)
	}
	inbounds := []xraycfg.Inbound{xraycfg.BuildTUNInbound(hasIPv6Stack()), xraycfg.BuildProbeInbound(pp.http)}
	raw, err := xraycfg.AssembleService(spec.Parts, inbounds, spec.LogLevel)
	if err != nil {
		return fmt.Errorf("конфигурация от интерфейса отклонена: %w", err)
	}
	if raw, err = finalizeConfig(raw, spec.AllowInsecure); err != nil {
		return err
	}
	bind, err := a.directBind()
	if err != nil {
		return fmt.Errorf("определить прямой путь мимо туннеля: %w", err)
	}
	if raw, err = xraycfg.BindDirectOutbounds(raw, bind); err != nil {
		return fmt.Errorf("привязать прямые outbound-ы: %w", err)
	}
	a.directBound = bind

	// The servers come from the outbounds the core runs, not from the client:
	// the routes kept off the tunnel and the kill switch's whitelist cannot
	// then name anything else.
	var servers []subscription.SubEntry
	for _, s := range xraycfg.OutboundServers(spec.Parts.Outbounds) {
		servers = append(servers, subscription.SubEntry{Protocol: s.Protocol, Address: s.Host, Port: s.Port})
	}
	if len(servers) == 0 {
		return errors.New("в outbounds нет ни одного сервера")
	}
	t := &target{profileName: spec.Title, profileRaw: raw, profileSrvs: servers}
	ports := sessionPorts{probe: pp.http}

	ch := make(chan tui.StatusUpdate, 64)
	a.setStatusCh(ch)
	// Updates go out in order on their own goroutine: publishers never wait,
	// and status does not either.
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for u := range ch {
			status(u)
		}
	}()
	defer func() {
		a.closeStatusCh(ch)
		<-forwarded
	}()

	slog.Info("──── служба: подключение: "+spec.Title+" ────", "binary", a.binary, "split", spec.Split, "kill_switch", spec.KillSwitch)
	a.runner = xray.New(a.binary, raw)

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()
	var xrayDone sync.WaitGroup
	var coreErr error
	defer func() {
		sessCancel()
		xrayDone.Wait()
		a.releaseSession()
	}()

	if err := a.runner.TestConfig(sessCtx); err != nil {
		return fmt.Errorf("проверка конфигурации xray не прошла: %w", explainInsecureRefusal(err))
	}
	gens := a.newTunLifecycle(t, ports, spec.Split)
	hooks := xray.RunHooks{AfterStart: gens.afterStart, AfterStop: gens.afterStop}
	xrayDone.Add(1)
	go func() {
		defer xrayDone.Done()
		err := a.runner.RunWithRetryHooks(sessCtx, 5, hooks)
		if err == nil || errors.Is(err, context.Canceled) || sessCtx.Err() != nil {
			return
		}
		slog.Error("xray runner failed", "error", err)
		coreErr = fmt.Errorf("ядро завершилось: %w", err)
		if gens.failed != nil {
			coreErr = gens.failed
		}
		sessCancel()
	}()

	select {
	case err := <-gens.ready:
		if err != nil {
			return err
		}
	case <-sessCtx.Done():
		sessCancel()
		xrayDone.Wait()
		if coreErr != nil {
			return coreErr
		}
		return sessCtx.Err()
	}
	ready(nil)
	<-sessCtx.Done()
	sessCancel()
	xrayDone.Wait()
	return coreErr
}
