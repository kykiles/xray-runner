// Package service is the privileged half of the program (H10): it holds the
// right to change the network — routes, the kill switch, the split's cgroup
// and nft rules, the core in TUN mode — and does it on behalf of the
// interface, which runs as the user and talks to it over package ipc.
//
// One session at a time. A session belongs to the connection that started it;
// when that connection drops, the session waits out a short grace period for
// the same user to take it back (ipc.Hello.Resume) and is taken down after it.
package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"xray-runner/internal/app"
	"xray-runner/internal/ipc"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xraycfg"
)

// Deps is what the service does to the machine; fields so tests can run it
// without root, a core or a network.
type Deps struct {
	Tun          func(ctx context.Context, spec app.ServiceTun, ready func(error), status func(tui.StatusUpdate)) error
	EnableSplit  func(names []string, uid int) (system.SplitScan, error)
	RefreshSplit func(names []string, uid int) (system.SplitScan, error)
	DisableSplit func() error
	CloseConns   func(conns []system.Conn, uid int) []system.Conn
	// ListenerUIDs are the owners of the local sockets bound to port on the
	// loopback or any address, network "tcp" (listeners) or "udp"; none when
	// nothing is bound there.
	ListenerUIDs func(network string, port int) []int
	Binary       string
	CoreVersion  string
	Version      string
	// Grace is how long a session outlives the connection that held it.
	Grace time.Duration
	// GeoDir is where the service keeps the geo databases interfaces hand
	// it (geoStore); empty to take none.
	GeoDir string
}

// DefaultGrace is Deps.Grace for the real service.
const DefaultGrace = 10 * time.Second

// Service serves connections one listener hands it.
type Service struct {
	d Deps
	// geo keeps the interfaces' geo databases; nil when the service takes
	// none.
	geo *geoStore

	// op is held across what a session does to the machine and the checks
	// it rests on — the split's rules, its teardown — so two of them never
	// interleave: a stop and the next enable, a refresh and a resume. Taken
	// before mu, never under it.
	op sync.Mutex

	mu    sync.Mutex
	sess  *session
	conns map[*ipc.Conn]bool
	// closing is set once Serve stops accepting: no new session after it.
	closing bool
	// logTo is the connection holding the session, for forwardLog. Apart
	// from mu: the service logs with mu held, and a log line must not wait
	// for the lock its own writer holds. Set by attached, under mu.
	logTo atomic.Pointer[ipc.Conn]
}

// attached records who holds the session now; with s.mu held, after every
// change to s.sess or its conn.
func (s *Service) attached() {
	if s.sess == nil {
		s.logTo.Store(nil)
		return
	}
	s.logTo.Store(s.sess.conn)
}

type session struct {
	kind  string // "tun" or "split"
	title string
	owner string // ipc.Peer.Key
	uid   int
	token string
	conn  *ipc.Conn // the connection holding it; nil during the grace period
	grace *time.Timer

	// stopping is set, under mu, by whoever takes the session down; nobody
	// takes it back after that. stoppedBy is the connection that asked for it
	// and gets its reply instead of an ended event; nil when the service
	// stopped it itself.
	stopping  bool
	stoppedBy *ipc.Conn
	// done is closed once the session is down.
	done chan struct{}

	// tun
	cancel context.CancelFunc
	// events are the status updates on their way to the interface, the
	// oldest dropped when it lags: each says what the screen shows now.
	events chan ipc.Event
	// ended is the connection told the session ended on its own, and why;
	// set before done is closed.
	endedTo  *ipc.Conn
	endedErr error
	// pumped is closed once pump has said all it had to.
	pumped chan struct{}

	// split
	names []string
}

// New makes a service over deps.
func New(d Deps) *Service {
	if d.Grace <= 0 {
		d.Grace = DefaultGrace
	}
	s := &Service{d: d, conns: map[*ipc.Conn]bool{}}
	if d.GeoDir != "" {
		g, err := newGeoStore(d.GeoDir)
		if err != nil {
			// Sessions still run, on the databases the service was installed
			// with; the hello tells interfaces not to send theirs.
			slog.Warn("служба: папка для гео-баз программы недоступна, работаю на своих", "dir", d.GeoDir, "error", err)
		} else {
			s.geo = g
		}
	}
	return s
}

// Serve accepts connections until ctx ends, then takes the session down and
// returns once the machine is left as it was found.
func (s *Service) Serve(ctx context.Context, l ipc.Listener) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	var wg sync.WaitGroup
	var err error
	for {
		c, aerr := l.Accept()
		if aerr != nil {
			if !errors.Is(aerr, ipc.ErrListenerClosed) && ctx.Err() == nil {
				err = aerr
			}
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(c)
		}()
	}
	// No new session from here on, then the current one down, then the
	// connections: a request still being handled finds no session to start.
	s.mu.Lock()
	s.closing = true
	sess := s.sess
	s.mu.Unlock()
	if sess != nil {
		s.teardown(sess)
		// The ended event goes out before the connection closes, unless
		// its client does not read.
		if sess.pumped != nil {
			select {
			case <-sess.pumped:
			case <-time.After(time.Second):
			}
		}
	}
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	wg.Wait()
	return err
}

func (s *Service) handle(c *ipc.Conn) {
	s.mu.Lock()
	s.conns[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()
	m, err := c.Read()
	if err != nil {
		var ve *ipc.VersionError
		if errors.As(err, &ve) {
			c.Reply(m.ID, nil, err)
		}
		return
	}
	if m.Type != ipc.TypeHello {
		c.Reply(m.ID, nil, errors.New("первым должно быть приветствие"))
		return
	}
	var h ipc.Hello
	if err := json.Unmarshal(m.Body, &h); err != nil || h.Version != ipc.Version {
		c.Reply(m.ID, nil, &ipc.VersionError{Got: h.Version})
		return
	}
	reply := ipc.HelloReply{Version: ipc.Version, ServiceVersion: s.d.Version, CoreVersion: s.d.CoreVersion, Geo: s.geo != nil}
	if h.Resume != "" {
		reply.Resumed = s.resume(c, h.Resume)
	}
	c.Reply(m.ID, reply, nil)
	slog.Info("служба: подключился интерфейс", "user", c.Peer.Name, "resumed", reply.Resumed)

	defer s.detach(c)
	// A database left half sent goes with the connection.
	var up *geoUpload
	defer func() { s.geo.abort(up) }()
	for {
		m, err := c.Read()
		if err != nil {
			return
		}
		switch m.Type {
		case ipc.TypeStartTun:
			s.startTun(c, m)
		case ipc.TypeStop:
			// In line: the next request of this connection sees the
			// session gone, not one on its way down.
			c.Reply(m.ID, nil, s.stop(c, ""))
		case ipc.TypeStatus:
			c.Reply(m.ID, s.status(c), nil)
		case ipc.TypeEnableSplit, ipc.TypeRefreshSplit:
			reply, err := s.split(c, m)
			c.Reply(m.ID, reply, err)
		case ipc.TypeDisableSplit:
			c.Reply(m.ID, nil, s.stop(c, "split"))
		case ipc.TypeCloseConns:
			reply, err := s.closeConns(c, m)
			c.Reply(m.ID, reply, err)
		case ipc.TypeGeoHave:
			reply, err := s.geoHave(m)
			c.Reply(m.ID, reply, err)
		case ipc.TypeGeoPut:
			up, err = s.geoPut(up, m)
			c.Reply(m.ID, nil, err)
		default:
			c.Reply(m.ID, nil, fmt.Errorf("неизвестный запрос %q", m.Type))
		}
	}
}

// resume hands the session named by token back to the same user's new
// connection. The old connection may not have been seen to drop yet — a
// client reconnects as soon as it notices, and the service can notice later —
// so it is taken over whether it waits in its grace period or not: the token
// went to that one connection only, and whoever holds it is its client. A
// session on its way down is not handed back: it would end under its new
// connection without a word.
func (s *Service) resume(c *ipc.Conn, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sess
	if sess == nil || sess.stopping || s.closing || sess.owner != c.Peer.Key ||
		subtle.ConstantTimeCompare([]byte(sess.token), []byte(token)) != 1 {
		return false
	}
	if sess.grace != nil {
		sess.grace.Stop()
		sess.grace = nil
	}
	if old := sess.conn; old != nil {
		old.Close()
	}
	sess.conn = c
	s.attached()
	slog.InfoContext(sessionLog, "служба: сессия возвращена интерфейсу", "user", c.Peer.Name, "kind", sess.kind)
	return true
}

// detach starts the grace period of the session c held.
func (s *Service) detach(c *ipc.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sess
	if sess == nil || sess.conn != c {
		return
	}
	sess.conn = nil
	s.attached()
	slog.Warn("служба: интерфейс отключился, сессия ждёт его", "user", c.Peer.Name, "grace", s.d.Grace)
	sess.grace = time.AfterFunc(s.d.Grace, func() {
		s.op.Lock()
		defer s.op.Unlock()
		// Orphaned and on its way down in one hold of mu: a resume after it
		// is refused, one before it leaves the session a connection.
		s.mu.Lock()
		orphan := s.sess == sess && sess.conn == nil && s.markStop(sess, nil)
		s.mu.Unlock()
		if orphan {
			slog.Warn("служба: интерфейс не вернулся, сессия снимается", "kind", sess.kind)
			s.finish(sess)
		}
	})
}

// claim makes a new session c's, or says whose the current one is.
func (s *Service) claim(c *ipc.Conn, sess *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("служба останавливается")
	}
	if cur := s.sess; cur != nil {
		if cur.conn == c {
			return fmt.Errorf("сессия (%s) уже идёт — сначала остановите её", cur.kind)
		}
		if cur.owner == c.Peer.Key {
			return errors.New("служба занята вашей сессией из другого окна")
		}
		return errors.New("служба занята сессией другого пользователя")
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	sess.token, sess.owner, sess.uid, sess.conn = token, c.Peer.Key, c.Peer.UID, c
	s.sess = sess
	s.attached()
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Service) startTun(c *ipc.Conn, m ipc.Message) {
	var req ipc.StartTun
	if err := json.Unmarshal(m.Body, &req); err != nil {
		c.Reply(m.ID, nil, fmt.Errorf("запрос не разобран: %w", err))
		return
	}
	spec, err := s.tunSpec(req)
	if err != nil {
		c.Reply(m.ID, nil, err)
		return
	}
	// Not under Serve's context: Serve takes the session down itself, in
	// its order, and the client hears why.
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		kind: "tun", title: spec.Title, cancel: cancel,
		done: make(chan struct{}), events: make(chan ipc.Event, statusBacklog), pumped: make(chan struct{}),
	}
	if err := s.claim(c, sess); err != nil {
		cancel()
		c.Reply(m.ID, nil, err)
		return
	}
	slog.InfoContext(sessionLog, "служба: TUN-сессия", "user", c.Peer.Name, "title", spec.Title, "geo", req.Geo != nil)
	ready := make(chan error, 1)
	go s.pump(sess, c, m.ID, ready)
	go func() {
		defer close(sess.done)
		signal := func(err error) {
			select {
			case ready <- err:
			default:
			}
		}
		// The session holds the service now, the only one there is: its geo
		// databases are made the pair its core reads, and any other goes.
		err := s.useGeo(&spec, req.Geo)
		if err != nil {
			signal(err)
		} else {
			err = s.d.Tun(ctx, spec, signal, func(u tui.StatusUpdate) {
				queueLatest(sess.events, statusEvent(u))
			})
		}
		cancel()
		s.mu.Lock()
		if s.sess == sess {
			s.sess = nil
			s.attached()
		}
		if sess.grace != nil {
			sess.grace.Stop()
		}
		// Whoever asked for the stop has its reply; anyone else holding the
		// session is told it ended.
		if sess.conn != sess.stoppedBy {
			sess.endedTo, sess.endedErr = sess.conn, err
		}
		s.mu.Unlock()
		if err != nil {
			slog.WarnContext(sessionLog, "служба: TUN-сессия завершилась", "error", err)
		}
	}()
}

// statusBacklog is how many status updates wait for an interface that lags.
const statusBacklog = 16

// queueLatest puts ev on q, dropping the oldest when q is full. One sender.
func queueLatest(q chan ipc.Event, ev ipc.Event) {
	for {
		select {
		case q <- ev:
			return
		default:
		}
		select {
		case <-q:
		default:
		}
	}
}

// pump carries a tun session's words to its interface: the answer to
// start_tun, the status updates, and the ended event. It alone waits for a
// client that does not read; the session comes and goes without it.
func (s *Service) pump(sess *session, c *ipc.Conn, id uint64, ready <-chan error) {
	defer close(sess.pumped)
	var err error
	select {
	case err = <-ready:
	case <-sess.done:
		select {
		case err = <-ready:
		default:
			err = errors.New("сессия завершилась, не поднявшись")
		}
	}
	if err != nil {
		c.Reply(id, nil, err)
	} else {
		c.Reply(id, ipc.StartReply{Token: sess.token}, nil)
	}
	for {
		select {
		case ev := <-sess.events:
			s.emit(sess, ev)
		case <-sess.done:
			// The last updates were queued before done closed.
			for len(sess.events) > 0 {
				s.emit(sess, <-sess.events)
			}
			s.mu.Lock()
			to, why := sess.endedTo, sess.endedErr
			s.mu.Unlock()
			if err == nil && to != nil {
				ev := ipc.Event{Kind: ipc.EventEnded}
				if why != nil && !errors.Is(why, context.Canceled) {
					ev.Error = why.Error()
				}
				to.Event(ev)
			}
			return
		}
	}
}

// tunSpec checks a request and turns it into what app.ServeTun runs.
func (s *Service) tunSpec(req ipc.StartTun) (app.ServiceTun, error) {
	if err := req.Parts.Validate(); err != nil {
		return app.ServiceTun{}, fmt.Errorf("конфигурация отклонена: %w", err)
	}
	if !validLogLevel(req.LogLevel) {
		return app.ServiceTun{}, fmt.Errorf("уровень лога ядра %q неизвестен", req.LogLevel)
	}
	if req.Split && !system.SplitOverTUN {
		return app.ServiceTun{}, errors.New("здесь маршрутизация по процессам не строится на туннеле")
	}
	urls, err := checkURLs(req.CheckURLs)
	if err != nil {
		return app.ServiceTun{}, err
	}
	if req.Geo != nil {
		if s.geo == nil {
			return app.ServiceTun{}, errGeoRefused
		}
		if !validSHA(req.Geo.IP) || !validSHA(req.Geo.Site) {
			return app.ServiceTun{}, errors.New("хеш гео-базы не годится")
		}
	}
	return app.ServiceTun{
		Parts:         req.Parts,
		LogLevel:      req.LogLevel,
		AllowInsecure: req.AllowInsecure,
		KillSwitch:    req.KillSwitch && !req.Split,
		Split:         req.Split,
		CheckURLs:     urls,
		Title:         cleanTitle(req.Title),
		Binary:        s.d.Binary,
	}, nil
}

// useGeo points spec at the pair of geo databases the session runs on, or at
// the service's own when geo is nil.
func (s *Service) useGeo(spec *app.ServiceTun, geo *ipc.GeoRef) error {
	if s.geo == nil {
		return nil // tunSpec refused a pair already
	}
	dir, err := s.geo.use(geo)
	if err != nil {
		return err
	}
	spec.GeoDir = dir
	return nil
}

// errGeoRefused answers a geo request to a service that takes no databases.
var errGeoRefused = errors.New("служба не принимает гео-базы программы")

// geoHave says which of the databases an interface is about to run on the
// service lacks.
func (s *Service) geoHave(m ipc.Message) (*ipc.GeoHaveReply, error) {
	if s.geo == nil {
		return nil, errGeoRefused
	}
	var req ipc.GeoHave
	if err := json.Unmarshal(m.Body, &req); err != nil {
		return nil, fmt.Errorf("запрос не разобран: %w", err)
	}
	missing, err := s.geo.missing(req.SHA256)
	if err != nil {
		return nil, err
	}
	return &ipc.GeoHaveReply{Missing: missing}, nil
}

// geoPut takes a piece of a database; up is the connection's upload in
// progress, and what is in progress after it is returned.
func (s *Service) geoPut(up *geoUpload, m ipc.Message) (*geoUpload, error) {
	if s.geo == nil {
		return nil, errGeoRefused
	}
	var req ipc.GeoPut
	if err := json.Unmarshal(m.Body, &req); err != nil {
		s.geo.abort(up)
		return nil, fmt.Errorf("запрос не разобран: %w", err)
	}
	return s.geo.put(up, req)
}

func validLogLevel(l string) bool {
	for _, v := range xraycfg.LogLevels {
		if l == v {
			return true
		}
	}
	return false
}

// checkURLs keeps the health check to plain web requests.
func checkURLs(in []string) ([]string, error) {
	if len(in) > 16 {
		return nil, errors.New("слишком много адресов проверки связи")
	}
	for _, raw := range in {
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("адрес проверки связи %q: нужен http(s)", raw)
		}
	}
	return in, nil
}

// cleanTitle keeps a title fit for one log line.
func cleanTitle(t string) string {
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, t)
	if r := []rune(t); len(r) > 120 {
		t = string(r[:120])
	}
	return t
}

func statusEvent(u tui.StatusUpdate) ipc.Event {
	return ipc.Event{
		Kind: ipc.EventStatus, OK: u.OK, LatencyMs: u.Latency.Milliseconds(),
		Note: u.Note, Err: u.Err, ResetHealth: u.ResetHealth, Generation: u.Generation,
	}
}

// emit sends ev to whoever holds sess now; nobody during the grace period.
func (s *Service) emit(sess *session, ev ipc.Event) {
	s.mu.Lock()
	conn := sess.conn
	s.mu.Unlock()
	if conn != nil {
		conn.Event(ev)
	}
}

// sessionLog marks a line of the service's own log as the session's, for its
// holder to see too (forwardLog); the rest — who connected, who was refused —
// is for the service's log alone.
var sessionLog = context.WithValue(context.Background(), sessionLogKey{}, true)

type sessionLogKey struct{}

// forwardLog hands a line of the session's log to the connection holding it:
// the core's output reaches the user's own log that way.
func (s *Service) forwardLog(level slog.Level, line string) {
	if conn := s.logTo.Load(); conn != nil {
		conn.Event(ipc.Event{Kind: ipc.EventLog, Level: level.String(), Line: line})
	}
}

// stop takes down c's session — only a split one with kind "split" — and
// returns once it is down.
func (s *Service) stop(c *ipc.Conn, kind string) error {
	s.op.Lock()
	s.mu.Lock()
	sess := s.sess
	if sess == nil || (kind != "" && sess.kind != kind) {
		s.mu.Unlock()
		s.op.Unlock()
		return nil
	}
	if sess.conn != c {
		s.mu.Unlock()
		s.op.Unlock()
		return errors.New("сессия не этого подключения")
	}
	first := s.markStop(sess, c)
	s.mu.Unlock()
	if first {
		s.finish(sess)
	}
	s.op.Unlock()
	<-sess.done
	return nil
}

// teardown takes sess down on the service's own account and waits until it
// is.
func (s *Service) teardown(sess *session) {
	s.op.Lock()
	s.mu.Lock()
	first := s.markStop(sess, nil)
	s.mu.Unlock()
	if first {
		s.finish(sess)
	}
	s.op.Unlock()
	<-sess.done
}

// markStop claims sess's teardown for the caller, false when someone has it
// already. With mu held.
func (s *Service) markStop(sess *session, by *ipc.Conn) bool {
	if sess.stopping {
		return false
	}
	sess.stopping, sess.stoppedBy = true, by
	if sess.grace != nil {
		sess.grace.Stop()
		sess.grace = nil
	}
	return true
}

// finish takes down the session markStop gave the caller. With op held.
func (s *Service) finish(sess *session) {
	switch sess.kind {
	case "tun":
		sess.cancel()
		<-sess.done
	case "split":
		if err := s.d.DisableSplit(); err != nil {
			slog.WarnContext(sessionLog, "служба: раздельная маршрутизация снята не полностью", "error", err)
		}
		s.drop(sess)
	}
}

// drop forgets a split session that is down.
func (s *Service) drop(sess *session) {
	s.mu.Lock()
	if s.sess == sess {
		s.sess = nil
		s.attached()
	}
	s.mu.Unlock()
	close(sess.done)
}

func (s *Service) status(c *ipc.Conn) ipc.StatusReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sess
	if sess == nil {
		return ipc.StatusReply{}
	}
	r := ipc.StatusReply{Active: true, Kind: sess.kind, Mine: sess.owner == c.Peer.Key, Attached: sess.conn != nil}
	if r.Mine {
		r.Title = sess.title
	}
	return r
}

// maxSplitNames bounds a split request.
const maxSplitNames = 256

func (s *Service) split(c *ipc.Conn, m ipc.Message) (*ipc.SplitReply, error) {
	if system.SplitOverTUN || c.Peer.UID < 0 {
		return nil, errors.New("здесь маршрутизация по процессам строится на туннеле — запрос не нужен")
	}
	var req ipc.SplitRequest
	if err := json.Unmarshal(m.Body, &req); err != nil {
		return nil, fmt.Errorf("запрос не разобран: %w", err)
	}
	if len(req.Names) > maxSplitNames {
		return nil, errors.New("слишком длинный список процессов")
	}
	for _, n := range req.Names {
		if n == "" || len(n) > 255 || strings.ContainsAny(n, "/\x00") {
			return nil, fmt.Errorf("имя процесса %q не годится", n)
		}
	}
	// root moves anyone's processes, as a run under sudo does; anybody else
	// only their own.
	uid := c.Peer.UID
	if uid == 0 {
		uid = -1
	}

	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	cur := s.sess
	mine := cur != nil && cur.kind == "split" && cur.conn == c && !cur.stopping
	s.mu.Unlock()
	if m.Type == ipc.TypeRefreshSplit {
		if !mine {
			return &ipc.SplitReply{}, nil
		}
		// The core may have restarted since the split went up, and somebody
		// else may have taken its ports in the gap: the split does not
		// outlive that.
		if err := s.redirectOwnedBy(c, false); err != nil {
			s.mu.Lock()
			first := s.markStop(cur, c)
			s.mu.Unlock()
			if first {
				slog.WarnContext(sessionLog, "служба: раздельная маршрутизация снята", "error", err)
				s.finish(cur)
			}
			return nil, fmt.Errorf("%w — раздельная маршрутизация снята", err)
		}
		scan, err := s.d.RefreshSplit(req.Names, uid)
		if err != nil {
			return nil, err
		}
		return &ipc.SplitReply{Token: cur.token, Matched: scan.Matched, Unclosed: scan.Unclosed, Moved: scan.Moved}, nil
	}

	if len(req.Names) == 0 {
		return &ipc.SplitReply{}, nil
	}
	if err := s.redirectOwnedBy(c, true); err != nil {
		return nil, err
	}
	sess := cur
	if !mine {
		sess = &session{kind: "split", title: "split", done: make(chan struct{})}
		if err := s.claim(c, sess); err != nil {
			return nil, err
		}
	}
	scan, err := s.d.EnableSplit(req.Names, uid)
	if err != nil {
		// EnableSplit leaves nothing behind when it fails.
		s.mu.Lock()
		first := s.markStop(sess, c)
		s.mu.Unlock()
		if first {
			s.drop(sess)
		}
		return nil, err
	}
	s.mu.Lock()
	sess.names = req.Names
	s.mu.Unlock()
	return &ipc.SplitReply{Token: sess.token, Matched: scan.Matched, Unclosed: scan.Unclosed, Moved: scan.Moved}, nil
}

// redirectOwnedBy checks that the ports the split redirects into — TCP for
// the traffic, UDP for DNS — are c's user's own: the moved processes'
// traffic and names go to whoever holds them. Nobody on them is an error
// only when the split is going up; later it is a core restarting.
func (s *Service) redirectOwnedBy(c *ipc.Conn, enabling bool) error {
	if c.Peer.UID == 0 {
		return nil
	}
	for _, p := range []struct {
		network string
		port    int
	}{{"tcp", xraycfg.RedirectPort}, {"udp", xraycfg.RedirectDNS}} {
		owners := s.d.ListenerUIDs(p.network, p.port)
		if len(owners) == 0 && enabling {
			return fmt.Errorf("на порту %s/%d никто не слушает — ядро интерфейса не запущено", p.network, p.port)
		}
		for _, uid := range owners {
			if uid != c.Peer.UID {
				return fmt.Errorf("порт %s/%d занял процесс другого пользователя (uid %d)", p.network, p.port, uid)
			}
		}
	}
	return nil
}

// maxConns bounds a close_conns request.
const maxConns = 4096

// closeConns closes connections of the split's owner that predate the move
// of their process; each is checked to be the owner's socket (system.CloseConns).
func (s *Service) closeConns(c *ipc.Conn, m ipc.Message) (*ipc.CloseConnsReply, error) {
	var req ipc.CloseConns
	if err := json.Unmarshal(m.Body, &req); err != nil {
		return nil, fmt.Errorf("запрос не разобран: %w", err)
	}
	if len(req.Conns) > maxConns {
		return nil, errors.New("слишком много соединений в запросе")
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	sess := s.sess
	mine := sess != nil && sess.kind == "split" && sess.conn == c && !sess.stopping
	s.mu.Unlock()
	if !mine {
		return nil, errors.New("нет раздельной маршрутизации этого подключения")
	}
	uid := c.Peer.UID
	if uid == 0 {
		uid = -1
	}
	return &ipc.CloseConnsReply{Open: s.d.CloseConns(req.Conns, uid)}, nil
}
