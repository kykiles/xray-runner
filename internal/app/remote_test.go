package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-runner/internal/ipc"
	"xray-runner/internal/system"
)

// fakeService stands in for the service's connection.
type fakeService struct {
	mu      sync.Mutex
	calls   []string
	start   ipc.StartTun
	startFn func() error
	events  chan ipc.Event
	done    chan struct{}
	hello   ipc.HelloReply
	stopped chan struct{}
	once    sync.Once
	// split
	moved    map[string]string
	open     []system.Conn
	closeReq ipc.CloseConns
}

func newFakeService() *fakeService {
	return &fakeService{
		events:  make(chan ipc.Event, 16),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
		hello:   ipc.HelloReply{Version: ipc.Version, CoreVersion: "Xray 26.7.28 (Xray, Penetrates Everything.)"},
	}
}

func (f *fakeService) Call(ctx context.Context, typ string, req, reply any) error {
	f.mu.Lock()
	f.calls = append(f.calls, typ)
	f.mu.Unlock()
	switch typ {
	case ipc.TypeStartTun:
		f.mu.Lock()
		f.start = req.(ipc.StartTun)
		fn := f.startFn
		f.mu.Unlock()
		if fn != nil {
			if err := fn(); err != nil {
				return err
			}
		}
		*reply.(*ipc.StartReply) = ipc.StartReply{Token: "tok"}
	case ipc.TypeStop:
		f.once.Do(func() { close(f.stopped) })
	case ipc.TypeEnableSplit, ipc.TypeRefreshSplit:
		f.mu.Lock()
		moved := f.moved
		f.moved = nil
		f.mu.Unlock()
		*reply.(*ipc.SplitReply) = ipc.SplitReply{Matched: req.(ipc.SplitRequest).Names, Moved: moved}
	case ipc.TypeCloseConns:
		f.mu.Lock()
		f.closeReq = req.(ipc.CloseConns)
		open := f.open
		f.mu.Unlock()
		*reply.(*ipc.CloseConnsReply) = ipc.CloseConnsReply{Open: open}
	}
	return nil
}

func (f *fakeService) Events() <-chan ipc.Event { return f.events }
func (f *fakeService) Done() <-chan struct{}    { return f.done }
func (f *fakeService) Close() error             { return nil }
func (f *fakeService) Hello() ipc.HelloReply    { return f.hello }

func (f *fakeService) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newRemoteApp(t *testing.T) (*App, *fakeService) {
	t.Helper()
	a := newTemplateApp(t)
	a.mode = "tun"
	a.noTTY = true
	a.cfg.KillSwitch = true
	a.cfg.XrayLogLvl = "warning"
	// Nothing on the machine is the interface's to touch in a service session.
	a.enableTunRouting = func(system.TunRouteConfig) error { t.Error("routes set up in the interface"); return nil }
	a.enableKillSwitch = func(system.KillSwitchConfig) error { t.Error("kill switch in the interface"); return nil }
	a.checkPrivileges = func() error { return errors.New("no privileges") }
	f := newFakeService()
	a.attachService(f)
	return a, f
}

// A tun session with a service goes to the service: the interface sends the
// parts of the config and the switches, and nothing else — no inbounds, no
// log paths — and asks for the session to stop on its way out.
func TestRemoteTunSession(t *testing.T) {
	a, f := newRemoteApp(t)
	if err := a.tunReady(); err != nil {
		t.Fatalf("tun refused with a service: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(ctx, coreSessionTarget())
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(f.callList(), ipc.TypeStartTun) {
		if time.Now().After(deadline) {
			t.Fatal("no start_tun")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A health result from the service reaches the screen's state.
	f.events <- ipc.Event{Kind: ipc.EventStatus, OK: true, LatencyMs: 30, Generation: 1}
	for {
		a.statusMu.Lock()
		ok := a.lastCheckOK
		a.statusMu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health event not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	select {
	case <-f.stopped:
	default:
		t.Fatal("session not stopped on the service")
	}

	f.mu.Lock()
	req := f.start
	f.mu.Unlock()
	if !req.KillSwitch || req.LogLevel != "warning" || req.Split || len(req.CheckURLs) == 0 {
		t.Fatalf("request %+v", req)
	}
	body, _ := json.Marshal(req)
	for _, leak := range []string{"inbounds", `"tun"`, "\"log\":"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("request carries %s: %s", leak, body)
		}
	}
	if len(req.Parts.Outbounds) == 0 {
		t.Fatal("no outbounds sent")
	}
}

// The service ending the session (the core gave up) ends it here too, with
// the service's reason.
func TestRemoteSessionEndedByService(t *testing.T) {
	a, f := newRemoteApp(t)
	f.startFn = func() error {
		go func() { f.events <- ipc.Event{Kind: ipc.EventEnded, Error: "ядро завершилось: boom"} }()
		return nil
	}
	_, err := a.runSession(context.Background(), coreSessionTarget())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

// A refused bring-up is the session's error, and the service is still asked
// to stop whatever it had started.
func TestRemoteSessionRefused(t *testing.T) {
	a, f := newRemoteApp(t)
	f.startFn = func() error {
		return &ipc.RemoteError{Msg: "служба занята сессией другого пользователя"}
	}
	_, err := a.runSession(context.Background(), coreSessionTarget())
	if err == nil || !strings.Contains(err.Error(), "занята") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(f.callList(), ipc.TypeStop) {
		t.Fatal("no stop after a refused start")
	}
}

// A service whose core is too old for TUN is refused like one without rights.
func TestServiceCoreTooOld(t *testing.T) {
	a, f := newRemoteApp(t)
	f.hello.CoreVersion = "Xray 1.8.0 (x)"
	if err := a.tunReady(); err == nil {
		t.Fatal("old core accepted")
	}
}

// Without a service, tun is judged by the process's own rights, as before.
func TestNoServiceKeepsLocalRights(t *testing.T) {
	a := newTemplateApp(t)
	a.checkPrivileges = func() error { return errors.New("no privileges") }
	t.Setenv(NoServiceEnv, "1")
	a.useService(context.Background())
	if a.service() != nil || a.tunReady() == nil {
		t.Fatal("tun allowed without rights or service")
	}
}

// On Linux the split's cgroup and rules go through the service.
func TestUseServiceRoutesSplit(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split rides on the tunnel here")
	}
	a := newTemplateApp(t)
	f := newFakeService()
	old := dialService
	dialService = func(context.Context, string) (serviceConn, error) { return f, nil }
	t.Cleanup(func() { dialService = old })
	a.useService(context.Background())
	scan, err := a.enableSplit([]string{"curl"}, 1, 2)
	if err != nil || !slices.Equal(scan.Matched, []string{"curl"}) {
		t.Fatalf("%v %v", scan, err)
	}
	_, _ = a.rescanSplit([]string{"curl"})
	_ = a.disableSplit()
	want := []string{ipc.TypeEnableSplit, ipc.TypeRefreshSplit, ipc.TypeDisableSplit}
	if got := f.callList(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// Processes the service moved into the split had connections from before the
// move: the interface lists them, the service closes them, and one that stays
// open keeps its process listed as unclosed while it runs.
func TestServiceSplitClosesOldConnections(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split rides on the tunnel here")
	}
	a := newTemplateApp(t)
	f := newFakeService()
	a.attachService(f)
	self := strconv.Itoa(os.Getpid())
	gone := "999999999"
	c1 := system.Conn{Src: "192.168.1.5:1", Dst: "1.1.1.1:443"}
	c2 := system.Conn{Src: "192.168.1.5:2", Dst: "1.1.1.1:443"}
	old := connsOf
	connsOf = func(pids map[string]bool) (map[system.Conn][]string, error) {
		if !pids[self] || !pids[gone] {
			t.Errorf("pids %v", pids)
		}
		return map[system.Conn][]string{c1: {self}, c2: {gone}}, nil
	}
	t.Cleanup(func() { connsOf = old })
	f.moved = map[string]string{self: "code", gone: "curl"}
	f.open = []system.Conn{c1, c2}

	scan, err := a.serviceEnableSplit([]string{"code", "curl"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.closeReq.Conns) != 2 {
		t.Fatalf("close request %v", f.closeReq)
	}
	// curl's process is gone, and its connection with it.
	if !slices.Equal(scan.Unclosed, []string{"code"}) {
		t.Fatalf("unclosed %v", scan.Unclosed)
	}
	// Still listed on the next scan, which moves nothing new.
	f.open = nil
	scan, err = a.serviceRefreshSplit([]string{"code"})
	if err != nil || !slices.Equal(scan.Unclosed, []string{"code"}) {
		t.Fatalf("rescan unclosed %v %v", scan.Unclosed, err)
	}
	_ = a.serviceDisableSplit()
	scan, _ = a.serviceRefreshSplit([]string{"code"})
	if len(scan.Unclosed) != 0 {
		t.Fatalf("unclosed after disable %v", scan.Unclosed)
	}
}
