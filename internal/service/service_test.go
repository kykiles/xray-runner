package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xray-runner/internal/app"
	"xray-runner/internal/ipc"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
)

// pipeListener hands out in-memory connections.
type pipeListener struct {
	ch     chan *ipc.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *pipeListener) Accept() (*ipc.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, ipc.ErrListenerClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

// fakeMachine records what the service did to the machine.
type fakeMachine struct {
	mu        sync.Mutex
	tunUp     int
	tunDown   int
	splitOn   bool
	splitUID  int
	splitCall int
	lastSpec  app.ServiceTun
	listener  int // uid on the redirect port; -1 for none
	dns       int // uid on the DNS redirect port; -1 for none
	closeUID  int
	// slow is how long taking a session down takes: the core stopping, the
	// split's rules coming out.
	slow        time.Duration
	tunStopping int
	// tun replaces the session's run when set.
	tun func(ctx context.Context, ready func(error), status func(tui.StatusUpdate)) error
}

func (m *fakeMachine) deps() Deps {
	return Deps{
		Tun: func(ctx context.Context, spec app.ServiceTun, ready func(error), status func(tui.StatusUpdate)) error {
			m.mu.Lock()
			m.tunUp++
			m.lastSpec = spec
			tun, slow := m.tun, m.slow
			m.mu.Unlock()
			if tun != nil {
				return tun(ctx, ready, status)
			}
			ready(nil)
			status(tui.StatusUpdate{OK: true, Latency: 42 * time.Millisecond, Generation: 1})
			<-ctx.Done()
			m.mu.Lock()
			m.tunStopping++
			m.mu.Unlock()
			time.Sleep(slow)
			m.mu.Lock()
			m.tunDown++
			m.mu.Unlock()
			return ctx.Err()
		},
		EnableSplit: func(names []string, uid int) (system.SplitScan, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.splitOn, m.splitUID = true, uid
			m.splitCall++
			return system.SplitScan{Matched: names}, nil
		},
		RefreshSplit: func(names []string, uid int) (system.SplitScan, error) {
			return system.SplitScan{Matched: names}, nil
		},
		CloseConns: func(conns []system.Conn, uid int) []system.Conn {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.closeUID = uid
			return conns[1:]
		},
		DisableSplit: func() error {
			m.mu.Lock()
			slow := m.slow
			m.mu.Unlock()
			time.Sleep(slow)
			m.mu.Lock()
			defer m.mu.Unlock()
			m.splitOn = false
			return nil
		},
		ListenerUIDs: func(network string, port int) []int {
			m.mu.Lock()
			defer m.mu.Unlock()
			uid := m.listener
			if network == "udp" {
				uid = m.dns
			}
			if uid < 0 {
				return nil
			}
			return []int{uid}
		},
		Binary:      "/svc/xray",
		CoreVersion: "Xray 26.7.28",
		Version:     "test",
		Grace:       200 * time.Millisecond,
	}
}

func (m *fakeMachine) counts() (up, down int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tunUp, m.tunDown
}

type rig struct {
	t      *testing.T
	l      *pipeListener
	m      *fakeMachine
	cancel context.CancelFunc
	done   chan error
}

func (m *fakeMachine) set(f func(m *fakeMachine)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(m)
}

func (m *fakeMachine) split() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.splitOn
}

func newRig(t *testing.T) *rig { return newRigWith(t, nil) }

// newRigWith is newRig with the deps adjusted by adjust.
func newRigWith(t *testing.T, adjust func(*Deps)) *rig {
	m := &fakeMachine{listener: 1000, dns: 1000}
	l := &pipeListener{ch: make(chan *ipc.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, l: l, m: m, cancel: cancel, done: make(chan error, 1)}
	d := m.deps()
	if adjust != nil {
		adjust(&d)
	}
	s := New(d)
	// The log goes to the session's connection, as in the real service: a
	// line logged under the service's lock must not wait on it.
	old := slog.Default()
	slog.SetDefault(slog.New(&teeHandler{Handler: slog.NewTextHandler(io.Discard, nil), fwd: s.forwardLog}))
	t.Cleanup(func() { slog.SetDefault(old) })
	go func() { r.done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return r
}

// connect opens a connection as peer and says hello.
func (r *rig) connect(peer ipc.Peer, resume string) *ipc.Client {
	r.t.Helper()
	cli, srv := net.Pipe()
	r.l.ch <- ipc.NewConn(srv, peer)
	c := ipc.NewClient(cli)
	r.t.Cleanup(func() { _ = c.Close() })
	var h ipc.HelloReply
	if err := c.Call(ctxT(r.t), ipc.TypeHello, ipc.Hello{Version: ipc.Version, Resume: resume}, &h); err != nil {
		r.t.Fatal(err)
	}
	if resume != "" && !h.Resumed {
		r.t.Fatal("session not resumed")
	}
	return c
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var (
	alice = ipc.Peer{Key: "uid:1000", UID: 1000, Name: "alice"}
	bob   = ipc.Peer{Key: "uid:1001", UID: 1001, Name: "bob"}
	root  = ipc.Peer{Key: "uid:0", UID: 0, Name: "root"}
)

func tunReq() ipc.StartTun {
	var req ipc.StartTun
	req.Parts.Outbounds = json.RawMessage(`[{"protocol":"vless","settings":{"vnext":[{"address":"vpn.example.com","port":443}]}}]`)
	req.LogLevel = "warning"
	req.KillSwitch = true
	req.CheckURLs = []string{"https://example.com/generate_204"}
	req.Title = "srv\x1b[31m"
	return req
}

func start(t *testing.T, c *ipc.Client) string {
	t.Helper()
	var rep ipc.StartReply
	if err := c.Call(ctxT(t), ipc.TypeStartTun, tunReq(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Token) != 64 {
		t.Fatalf("token %q", rep.Token)
	}
	return rep.Token
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHelloFirst(t *testing.T) {
	r := newRig(t)
	cli, srv := net.Pipe()
	r.l.ch <- ipc.NewConn(srv, alice)
	c := ipc.NewClient(cli)
	defer func() { _ = c.Close() }()
	if err := c.Call(ctxT(t), ipc.TypeStatus, struct{}{}, nil); err == nil {
		t.Fatal("a request before hello was served")
	}
}

func TestStartStop(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	start(t, c)
	// The status update reaches the interface as an event, and so does the
	// service's log of the session.
	sawLog := false
	for done := false; !done; {
		select {
		case ev := <-c.Events():
			switch ev.Kind {
			case ipc.EventLog:
				sawLog = sawLog || strings.Contains(ev.Line, "TUN-сессия")
			case ipc.EventStatus:
				if !ev.OK || ev.LatencyMs != 42 {
					t.Fatalf("event %+v", ev)
				}
				done = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no status event")
		}
	}
	if !sawLog {
		t.Error("the session's log line did not reach the interface")
	}
	r.m.mu.Lock()
	spec := r.m.lastSpec
	r.m.mu.Unlock()
	if spec.Binary != "/svc/xray" || spec.Title != "srv[31m" || !spec.KillSwitch {
		t.Fatalf("spec %+v", spec)
	}
	if err := c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	// Stop returns once the session is down.
	if up, down := r.m.counts(); up != 1 || down != 1 {
		t.Fatalf("up %d down %d", up, down)
	}
}

// One session at a time: another user, or another window of the same one, is
// told the service is busy.
func TestOneSessionAtATime(t *testing.T) {
	r := newRig(t)
	a := r.connect(alice, "")
	start(t, a)
	for _, p := range []ipc.Peer{bob, alice} {
		c := r.connect(p, "")
		err := c.Call(ctxT(t), ipc.TypeStartTun, tunReq(), nil)
		if err == nil || !strings.Contains(err.Error(), "занята") {
			t.Fatalf("%s: err = %v", p.Name, err)
		}
		// Nor can it stop what is not its own.
		if err := c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err == nil {
			t.Fatalf("%s stopped someone else's session", p.Name)
		}
		var st ipc.StatusReply
		if err := c.Call(ctxT(t), ipc.TypeStatus, struct{}{}, &st); err != nil || !st.Active || st.Mine != (p == alice) {
			t.Fatalf("%s: status %+v %v", p.Name, st, err)
		}
		if p == bob && st.Title != "" {
			t.Fatalf("another user's title leaked: %q", st.Title)
		}
	}
	if up, _ := r.m.counts(); up != 1 {
		t.Fatalf("%d sessions started", up)
	}
}

// A connection that drops takes its session with it after the grace period.
func TestDisconnectTearsDownAfterGrace(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	start(t, c)
	_ = c.Close()
	time.Sleep(50 * time.Millisecond)
	if _, down := r.m.counts(); down != 0 {
		t.Fatal("torn down before the grace period ran out")
	}
	waitFor(t, func() bool { _, down := r.m.counts(); return down == 1 }, "session outlived its grace period")
}

// The same user coming back within the grace period with the token gets the
// session back; somebody else with the token does not.
func TestResume(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	token := start(t, c)
	_ = c.Close()

	cli, srv := net.Pipe()
	r.l.ch <- ipc.NewConn(srv, bob)
	b := ipc.NewClient(cli)
	defer func() { _ = b.Close() }()
	var h ipc.HelloReply
	if err := b.Call(ctxT(t), ipc.TypeHello, ipc.Hello{Version: ipc.Version, Resume: token}, &h); err != nil || h.Resumed {
		t.Fatalf("another user resumed the session: %+v %v", h, err)
	}

	c2 := r.connect(alice, token)
	time.Sleep(400 * time.Millisecond) // past the grace period
	if _, down := r.m.counts(); down != 0 {
		t.Fatal("a resumed session was torn down")
	}
	if err := c2.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStartTunRefusesBadRequests(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	bad := []func(*ipc.StartTun){
		func(q *ipc.StartTun) {
			q.Parts.Outbounds = json.RawMessage(`[{"protocol":"vless","settings":{"vnext":[{"address":"/run/x.sock"}]}}]`)
		},
		func(q *ipc.StartTun) { q.LogLevel = "trace" },
		func(q *ipc.StartTun) { q.CheckURLs = []string{"file:///etc/shadow"} },
	}
	for i, mut := range bad {
		req := tunReq()
		mut(&req)
		if err := c.Call(ctxT(t), ipc.TypeStartTun, req, nil); err == nil {
			t.Errorf("request %d accepted", i)
		}
	}
	if up, _ := r.m.counts(); up != 0 {
		t.Fatal("a refused request started a session")
	}
}

// The service moves only the asking user's processes, and only into a core
// of theirs: the redirect port must be held by the same user.
func TestSplitIsTheCallersOwn(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	c := r.connect(alice, "")
	req := ipc.SplitRequest{Names: []string{"telegram-desktop"}}

	r.m.mu.Lock()
	r.m.listener = bob.UID
	r.m.mu.Unlock()
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err == nil {
		t.Fatal("split into another user's listener")
	}
	r.m.mu.Lock()
	r.m.listener = alice.UID
	r.m.mu.Unlock()
	var rep ipc.SplitReply
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, &rep); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	uid := r.m.splitUID
	r.m.mu.Unlock()
	if uid != alice.UID || !slices.Equal(rep.Matched, req.Names) {
		t.Fatalf("uid %d, matched %v", uid, rep.Matched)
	}
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, ipc.SplitRequest{Names: []string{"../x"}}, nil); err == nil {
		t.Fatal("a path accepted as a process name")
	}
	if err := c.Call(ctxT(t), ipc.TypeDisableSplit, struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	on := r.m.splitOn
	r.m.mu.Unlock()
	if on {
		t.Fatal("split still on")
	}

	// root moves anyone's.
	rc := r.connect(root, "")
	if err := rc.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	uid = r.m.splitUID
	r.m.mu.Unlock()
	if uid != -1 {
		t.Fatalf("root's split limited to uid %d", uid)
	}
}

// A split session is taken down with its connection too.
func TestSplitGoesWithConnection(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	c := r.connect(alice, "")
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, ipc.SplitRequest{Names: []string{"curl"}}, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	waitFor(t, func() bool {
		r.m.mu.Lock()
		defer r.m.mu.Unlock()
		return !r.m.splitOn
	}, "split outlived its connection")
}

// Stopping the service takes the session down before Serve returns.
func TestServeEndsSession(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	start(t, c)
	r.cancel()
	select {
	case err := <-r.done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		r.done <- nil // for the cleanup
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if _, down := r.m.counts(); down != 1 {
		t.Fatal("session left up")
	}
}

// A client that reconnects before the service has noticed its old connection
// drop still gets its session back: the token is proof enough, and the old
// connection is dropped from the session.
func TestResumeBeforeDropNoticed(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	token := start(t, c)
	c2 := r.connect(alice, token) // the old connection is still open
	if err := c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err == nil {
		t.Fatal("the replaced connection still controls the session")
	}
	_ = c.Close()
	time.Sleep(400 * time.Millisecond) // past the grace period
	if _, down := r.m.counts(); down != 0 {
		t.Fatal("the old connection's drop took the resumed session down")
	}
	if err := c2.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
}

// Old connections are closed only for the split's own connection, and as the
// asking user: the service checks each socket against that uid.
func TestCloseConns(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	c := r.connect(alice, "")
	req := ipc.CloseConns{Conns: []system.Conn{{Src: "a:1", Dst: "b:2"}, {Src: "c:3", Dst: "d:4"}}}
	if err := c.Call(ctxT(t), ipc.TypeCloseConns, req, nil); err == nil {
		t.Fatal("closed connections without a split")
	}
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, ipc.SplitRequest{Names: []string{"curl"}}, nil); err != nil {
		t.Fatal(err)
	}
	other := r.connect(alice, "")
	if err := other.Call(ctxT(t), ipc.TypeCloseConns, req, nil); err == nil {
		t.Fatal("another connection closed the split's connections")
	}
	var rep ipc.CloseConnsReply
	if err := c.Call(ctxT(t), ipc.TypeCloseConns, req, &rep); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	uid := r.m.closeUID
	r.m.mu.Unlock()
	if uid != alice.UID || len(rep.Open) != 1 || rep.Open[0].Src != "c:3" {
		t.Fatalf("uid %d, open %v", uid, rep.Open)
	}
}

// hello says hello on c, asking for token's session back, and reports whether
// it was.
func hello(t *testing.T, c *ipc.Client, token string) bool {
	t.Helper()
	var h ipc.HelloReply
	if err := c.Call(ctxT(t), ipc.TypeHello, ipc.Hello{Version: ipc.Version, Resume: token}, &h); err != nil {
		t.Fatal(err)
	}
	return h.Resumed
}

// dial opens a connection as peer without saying hello.
func (r *rig) dial(peer ipc.Peer) *ipc.Client {
	cli, srv := net.Pipe()
	r.l.ch <- ipc.NewConn(srv, peer)
	c := ipc.NewClient(cli)
	r.t.Cleanup(func() { _ = c.Close() })
	return c
}

// A session on its way down is not handed back: the client would think its
// tunnel up while nothing is (review H10, 9).
func TestNoResumeDuringTeardown(t *testing.T) {
	r := newRig(t)
	r.m.set(func(m *fakeMachine) { m.slow = 300 * time.Millisecond })
	c := r.connect(alice, "")
	token := start(t, c)
	_ = c.Close()
	waitFor(t, func() bool {
		r.m.mu.Lock()
		defer r.m.mu.Unlock()
		return r.m.tunStopping == 1
	}, "the grace period did not take the session down")
	if hello(t, r.dial(alice), token) {
		t.Fatal("a session on its way down was resumed")
	}
	waitFor(t, func() bool { _, down := r.m.counts(); return down == 1 }, "session not down")
}

// Coming back just as the grace period runs out either gets the session back
// for good or not at all.
func TestResumeAtGraceExpiry(t *testing.T) {
	const grace = 30 * time.Millisecond
	for i := range 20 {
		func() {
			r := newRigWith(t, func(d *Deps) { d.Grace = grace })
			r.m.set(func(m *fakeMachine) { m.slow = 20 * time.Millisecond })
			c := r.connect(alice, "")
			token := start(t, c)
			_ = c.Close()
			time.Sleep(grace - 10*time.Millisecond + time.Duration(i)*time.Millisecond)
			c2 := r.dial(alice)
			resumed := hello(t, c2, token)
			time.Sleep(3 * grace)
			_, down := r.m.counts()
			if resumed && down != 0 {
				t.Fatalf("attempt %d: the resumed session was taken down", i)
			}
			if !resumed && down == 0 {
				waitFor(t, func() bool { _, down := r.m.counts(); return down == 1 }, "an orphan outlived its grace period")
			}
			r.cancel()
			<-r.done
			r.done <- nil
		}()
	}
}

// When the service ends a session its client did not ask to end, the client
// is told.
func TestEndedOnServiceStop(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	start(t, c)
	r.cancel()
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("connection closed without an ended event")
			}
			if ev.Kind == ipc.EventEnded {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no ended event")
		}
	}
}

// A stop and an enable_split right after it from the same connection run one
// after the other: the rules the enable puts in have a session to take them
// out (review H10, 10).
func TestStopThenEnableSplit(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	r.m.set(func(m *fakeMachine) { m.slow = 50 * time.Millisecond })
	c := r.connect(alice, "")
	req := ipc.SplitRequest{Names: []string{"curl"}}
	for i := range 5 {
		if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err != nil {
			t.Fatal(err)
		}
		stopped := make(chan error, 1)
		go func() { stopped <- c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil) }()
		time.Sleep(5 * time.Millisecond)
		if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		if !r.m.split() {
			t.Fatalf("attempt %d: the enable after the stop left no rules", i)
		}
		var st ipc.StatusReply
		if err := c.Call(ctxT(t), ipc.TypeStatus, struct{}{}, &st); err != nil || !st.Active || st.Kind != "split" {
			t.Fatalf("attempt %d: rules without a session: %+v %v", i, st, err)
		}
		if err := c.Call(ctxT(t), ipc.TypeDisableSplit, struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
		if r.m.split() {
			t.Fatalf("attempt %d: split still on", i)
		}
	}
}

// Once Serve is stopping no new session starts: the service does not exit
// with rules in place (review H10, 11).
func TestNoSplitWhileServeStops(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	r.m.set(func(m *fakeMachine) { m.slow = 200 * time.Millisecond })
	a := r.connect(alice, "")
	start(t, a)
	b := r.connect(alice, "")
	r.cancel()
	req := ipc.SplitRequest{Names: []string{"curl"}}
	for {
		err := b.Call(ctxT(t), ipc.TypeEnableSplit, req, nil)
		if err == nil {
			// Taken down by Serve, or not taken at all.
			break
		}
		if !strings.Contains(err.Error(), "занята") {
			break
		}
	}
	select {
	case <-r.done:
		r.done <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	r.m.mu.Lock()
	on, calls := r.m.splitOn, r.m.splitCall
	r.m.mu.Unlock()
	if on {
		t.Fatalf("the service stopped with the split on (%d enables)", calls)
	}
}

// Somebody else taking the redirect ports — the core's TCP one or its DNS
// one — while the split is on takes the split down (review H10, 8).
func TestSplitPortOwner(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("the split is built on the tunnel here")
	}
	r := newRig(t)
	c := r.connect(alice, "")
	req := ipc.SplitRequest{Names: []string{"curl"}}

	r.m.set(func(m *fakeMachine) { m.dns = bob.UID })
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err == nil {
		t.Fatal("split into another user's DNS listener")
	}
	r.m.set(func(m *fakeMachine) { m.dns = -1 })
	if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err == nil {
		t.Fatal("split with nobody on the DNS port")
	}

	for _, take := range []func(m *fakeMachine){
		func(m *fakeMachine) { m.listener = bob.UID },
		func(m *fakeMachine) { m.dns = bob.UID },
	} {
		r.m.set(func(m *fakeMachine) { m.listener, m.dns = alice.UID, alice.UID })
		if err := c.Call(ctxT(t), ipc.TypeEnableSplit, req, nil); err != nil {
			t.Fatal(err)
		}
		// The core restarting leaves the ports empty for a moment: that is
		// no reason to take the split down.
		r.m.set(func(m *fakeMachine) { m.listener, m.dns = -1, -1 })
		if err := c.Call(ctxT(t), ipc.TypeRefreshSplit, req, nil); err != nil {
			t.Fatal(err)
		}
		r.m.set(func(m *fakeMachine) { m.listener, m.dns = alice.UID, alice.UID })
		r.m.set(take)
		if err := c.Call(ctxT(t), ipc.TypeRefreshSplit, req, nil); err == nil {
			t.Fatal("refresh with another user on the port")
		}
		if r.m.split() {
			t.Fatal("split left on into another user's port")
		}
		var st ipc.StatusReply
		if err := c.Call(ctxT(t), ipc.TypeStatus, struct{}{}, &st); err != nil || st.Active {
			t.Fatalf("session left: %+v %v", st, err)
		}
	}
}

// An owner that stops reading holds up nobody but itself: its session still
// ends, and the service is free for the next one (review H10, 6).
func TestSilentOwnerHoldsNothing(t *testing.T) {
	r := newRig(t)
	r.m.set(func(m *fakeMachine) {
		m.tun = func(ctx context.Context, ready func(error), status func(tui.StatusUpdate)) error {
			ready(nil)
			for i := range 2000 {
				status(tui.StatusUpdate{OK: true, Generation: i})
			}
			return errors.New("ядро упало")
		}
	})
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = cli.Close() })
	r.l.ch <- ipc.NewConn(srv, alice)
	send := func(id uint64, typ string, body any) {
		data, _ := json.Marshal(body)
		if err := ipc.WriteMessage(cli, ipc.Message{ID: id, Type: typ, Body: data}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, ipc.TypeHello, ipc.Hello{Version: ipc.Version})
	send(2, ipc.TypeStartTun, tunReq())
	// Nothing is ever read on cli.
	waitFor(t, func() bool { up, _ := r.m.counts(); return up == 1 }, "the session did not start")

	b := r.connect(bob, "")
	waitFor(t, func() bool {
		var st ipc.StatusReply
		if err := b.Call(ctxT(t), ipc.TypeStatus, struct{}{}, &st); err != nil {
			t.Fatal(err)
		}
		return !st.Active
	}, "the session of a client that does not read never ended")
	r.m.set(func(m *fakeMachine) { m.tun = nil })
	start(t, b)
}

// The session's holder sees the session's lines, not the rest of the
// service's log (review H10, 13).
func TestLogOnlyTheSessions(t *testing.T) {
	r := newRig(t)
	c := r.connect(alice, "")
	start(t, c)
	r.connect(bob, "")
	if err := c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	sawSession := false
	for {
		select {
		case ev := <-c.Events():
			if ev.Kind != ipc.EventLog {
				continue
			}
			if strings.Contains(ev.Line, "bob") || strings.Contains(ev.Line, "подключился") {
				t.Fatalf("the service's own line reached the session: %q", ev.Line)
			}
			sawSession = sawSession || strings.Contains(ev.Line, "TUN-сессия")
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	if !sawSession {
		t.Error("the session's line did not reach it")
	}
}

func TestSessionCode(t *testing.T) {
	for fn, want := range map[string]bool{
		"xray-runner/internal/xray.(*Runner).pump":       true,
		"xray-runner/internal/app.serveTun.func2":        true,
		"xray-runner/internal/system.EnableSplitFor":     true,
		"xray-runner/internal/xraycfg.AssembleService":   false,
		"xray-runner/internal/service.(*Service).handle": false,
		"xray-runner/internal/ipc.Refuse":                false,
		"example.com/other/internal/app.Foo":             false,
		"main.main":                                      false,
	} {
		if got := sessionFunc(fn); got != want {
			t.Errorf("%s: %v, want %v", fn, got, want)
		}
	}
}
