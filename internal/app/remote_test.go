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
	"xray-runner/internal/tui"
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
	moved      map[string]string
	open       []system.Conn
	closeReq   ipc.CloseConns
	splitToken string
	splitErr   error
	lost       sync.Once
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
		token, err := f.splitToken, f.splitErr
		f.mu.Unlock()
		if err != nil {
			return err
		}
		*reply.(*ipc.SplitReply) = ipc.SplitReply{Token: token, Matched: req.(ipc.SplitRequest).Names, Moved: moved}
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

// drop is the connection going: the service restarted, or the socket broke.
func (f *fakeService) drop() { f.lost.Do(func() { close(f.done) }) }

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

// fakeDial makes dialService hand out next, recording the resume token asked
// for; err, when set, is a service that is gone.
func fakeDial(t *testing.T, next *fakeService, err error) *[]string {
	t.Helper()
	var mu sync.Mutex
	var resumes []string
	old := dialService
	dialService = func(_ context.Context, resume string) (serviceConn, error) {
		mu.Lock()
		resumes = append(resumes, resume)
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		return next, nil
	}
	t.Cleanup(func() { dialService = old })
	return &resumes
}

// newServiceSplitApp is a proxy session's split running through f, the way
// bringUpSplit leaves it, with the status channel open to read notes from.
func newServiceSplitApp(t *testing.T, f *fakeService) *App {
	t.Helper()
	if system.SplitOverTUN {
		t.Skip("the split rides on the tunnel here")
	}
	a := newTemplateApp(t)
	fakeDial(t, f, nil)
	a.useService(context.Background())
	a.splitApps = []string{"curl"}
	a.bringUpSplit()
	if !a.splitState() {
		t.Fatalf("split not on: %q", a.pendingNote)
	}
	a.statusCh = make(chan tui.StatusUpdate, 64)
	return a
}

// A split through the service survives the connection dropping: the
// interface dials again with the split's token, and the service hands the
// split back.
func TestServiceSplitResumedAfterDrop(t *testing.T) {
	f := newFakeService()
	f.splitToken = "stok"
	a := newServiceSplitApp(t, f)
	next := newFakeService()
	next.hello.Resumed = true
	resumes := fakeDial(t, next, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.splitRescanLoop(ctx); close(done) }()
	f.drop()
	waitFor(t, nil, func() bool {
		return slices.Contains(next.callList(), ipc.TypeRefreshSplit)
	}, "a rescan on the resumed connection")
	cancel()
	<-done
	if !a.splitState() {
		t.Fatal("split off after a resume")
	}
	if len(*resumes) == 0 || (*resumes)[0] != "stok" {
		t.Fatalf("resume tokens %v", *resumes)
	}
}

// A split the service did not hand back is switched off, on the screen, and
// teardown has nothing to take down.
func TestServiceSplitLostAfterDrop(t *testing.T) {
	f := newFakeService()
	f.splitToken = "stok"
	a := newServiceSplitApp(t, f)
	next := newFakeService()
	fakeDial(t, next, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.splitRescanLoop(ctx); close(done) }()
	f.drop()
	waitFor(t, nil, func() bool { return !a.splitState() }, "the split to go off")
	cancel()
	<-done
	var note string
	for len(a.statusCh) > 0 {
		if u := <-a.statusCh; u.Note != "" {
			note = u.Note
		}
	}
	if !strings.Contains(note, "не вернулась") || !strings.Contains(note, "напрямую") {
		t.Fatalf("note %q", note)
	}
	a.releaseSession()
	if slices.Contains(next.callList(), ipc.TypeDisableSplit) {
		t.Fatal("split the service no longer has taken down again")
	}
}

// A rescan the service refuses is said on the screen, once, not every second.
func TestServiceSplitRescanErrorShownOnce(t *testing.T) {
	f := newFakeService()
	a := newServiceSplitApp(t, f)
	f.mu.Lock()
	f.splitErr = &ipc.RemoteError{Msg: "cgroup недоступна"}
	f.mu.Unlock()
	for range 3 {
		a.refreshSplit()
	}
	notes := 0
	for len(a.statusCh) > 0 {
		if u := <-a.statusCh; strings.Contains(u.Note, "cgroup недоступна") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("%d notes for one failure", notes)
	}
}

// A connection that went while no session ran is dialled again before the
// next one, which goes to the service as usual.
func TestServiceRedialedBetweenSessions(t *testing.T) {
	a, f := newRemoteApp(t)
	next := newFakeService()
	fakeDial(t, next, nil)
	f.drop()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(ctx, coreSessionTarget())
		done <- err
	}()
	waitFor(t, done, func() bool {
		return slices.Contains(next.callList(), ipc.TypeStartTun)
	}, "start_tun on the new connection")
	cancel()
	<-done
	if slices.Contains(f.callList(), ipc.TypeStartTun) {
		t.Fatal("start_tun sent on the dead connection")
	}
}

// A service gone for good between sessions leaves the interface on its own:
// TUN is judged by its own rights again, and the split has its own functions
// back.
func TestServiceGoneBetweenSessions(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	a.noTTY = true
	a.checkPrivileges = func() error { return errors.New("no privileges") }
	localSplit := false
	a.enableSplit = func([]string, int, int) (system.SplitScan, error) {
		localSplit = true
		return system.SplitScan{}, nil
	}
	f := newFakeService()
	fakeDial(t, f, nil)
	a.useService(context.Background())
	fakeDial(t, nil, errors.New("connection refused"))
	f.drop()

	_, err := a.runSession(context.Background(), coreSessionTarget())
	if err == nil || !strings.Contains(err.Error(), "больше недоступна") || !strings.Contains(err.Error(), "no privileges") {
		t.Fatalf("err = %v", err)
	}
	if a.service() != nil || a.tunReady() == nil {
		t.Fatal("tun allowed without rights or service")
	}
	_, _ = a.enableSplit(nil, 0, 0)
	if !system.SplitOverTUN && !localSplit {
		t.Fatal("split still goes to the gone service")
	}
}
