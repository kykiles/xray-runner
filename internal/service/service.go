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
	// ListenerUID is the owner of the local TCP listener on port, false when
	// nothing listens there.
	ListenerUID func(port int) (int, bool)
	Binary      string
	CoreVersion string
	Version     string
	// Grace is how long a session outlives the connection that held it.
	Grace time.Duration
}

// DefaultGrace is Deps.Grace for the real service.
const DefaultGrace = 10 * time.Second

// Service serves connections one listener hands it.
type Service struct {
	d   Deps
	ctx context.Context

	mu    sync.Mutex
	sess  *session
	conns map[*ipc.Conn]bool
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

	// tun
	cancel   context.CancelFunc
	done     chan struct{}
	stopping bool

	// split
	names []string
}

// New makes a service over deps.
func New(d Deps) *Service {
	if d.Grace <= 0 {
		d.Grace = DefaultGrace
	}
	return &Service{d: d, conns: map[*ipc.Conn]bool{}}
}

// Serve accepts connections until ctx ends, then takes the session down and
// returns once the machine is left as it was found.
func (s *Service) Serve(ctx context.Context, l ipc.Listener) error {
	s.ctx = ctx
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
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess != nil {
		s.teardown(sess)
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
	reply := ipc.HelloReply{Version: ipc.Version, ServiceVersion: s.d.Version, CoreVersion: s.d.CoreVersion}
	if h.Resume != "" {
		reply.Resumed = s.resume(c, h.Resume)
	}
	c.Reply(m.ID, reply, nil)
	slog.Info("служба: подключился интерфейс", "user", c.Peer.Name, "resumed", reply.Resumed)

	defer s.detach(c)
	for {
		m, err := c.Read()
		if err != nil {
			return
		}
		switch m.Type {
		case ipc.TypeStartTun:
			s.startTun(c, m)
		case ipc.TypeStop:
			go func() { c.Reply(m.ID, nil, s.stop(c)) }()
		case ipc.TypeStatus:
			c.Reply(m.ID, s.status(c), nil)
		case ipc.TypeEnableSplit, ipc.TypeRefreshSplit:
			reply, err := s.split(c, m)
			c.Reply(m.ID, reply, err)
		case ipc.TypeDisableSplit:
			c.Reply(m.ID, nil, s.disableSplit(c))
		case ipc.TypeCloseConns:
			reply, err := s.closeConns(c, m)
			c.Reply(m.ID, reply, err)
		default:
			c.Reply(m.ID, nil, fmt.Errorf("неизвестный запрос %q", m.Type))
		}
	}
}

// resume hands the session named by token back to the same user's new
// connection. The old connection may not have been seen to drop yet — a
// client reconnects as soon as it notices, and the service can notice later —
// so it is taken over whether it waits in its grace period or not: the token
// went to that one connection only, and whoever holds it is its client.
func (s *Service) resume(c *ipc.Conn, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sess
	if sess == nil || sess.owner != c.Peer.Key ||
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
	slog.Info("служба: сессия возвращена интерфейсу", "user", c.Peer.Name, "kind", sess.kind)
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
		s.mu.Lock()
		orphan := s.sess == sess && sess.conn == nil
		s.mu.Unlock()
		if orphan {
			slog.Warn("служба: интерфейс не вернулся, сессия снимается", "kind", sess.kind)
			s.teardown(sess)
		}
	})
}

// claim makes a new session c's, or says whose the current one is.
func (s *Service) claim(c *ipc.Conn, sess *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	ctx, cancel := context.WithCancel(s.ctx)
	sess := &session{kind: "tun", title: spec.Title, cancel: cancel, done: make(chan struct{})}
	if err := s.claim(c, sess); err != nil {
		cancel()
		c.Reply(m.ID, nil, err)
		return
	}
	slog.Info("служба: TUN-сессия", "user", c.Peer.Name, "title", spec.Title)
	go func() {
		defer close(sess.done)
		readied := false
		err := s.d.Tun(ctx, spec, func(err error) {
			readied = err == nil
			if err != nil {
				c.Reply(m.ID, nil, err)
				return
			}
			c.Reply(m.ID, ipc.StartReply{Token: sess.token}, nil)
		}, func(u tui.StatusUpdate) {
			s.emit(sess, statusEvent(u))
		})
		cancel()
		s.mu.Lock()
		if s.sess == sess {
			s.sess = nil
			s.attached()
		}
		if sess.grace != nil {
			sess.grace.Stop()
		}
		stopping, conn := sess.stopping, sess.conn
		s.mu.Unlock()
		if err != nil {
			slog.Warn("служба: TUN-сессия завершилась", "error", err)
		}
		if readied && !stopping && conn != nil {
			ev := ipc.Event{Kind: ipc.EventEnded}
			if err != nil && !errors.Is(err, context.Canceled) {
				ev.Error = err.Error()
			}
			conn.Event(ev)
		}
	}()
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

// forwardLog hands a line of the service's log to the connection holding the
// session: the core's output reaches the user's own log that way.
func (s *Service) forwardLog(level slog.Level, line string) {
	if conn := s.logTo.Load(); conn != nil {
		conn.Event(ipc.Event{Kind: ipc.EventLog, Level: level.String(), Line: line})
	}
}

func (s *Service) stop(c *ipc.Conn) error {
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil {
		return nil
	}
	if sess.conn != c {
		return errors.New("сессия не этого подключения")
	}
	s.teardown(sess)
	return nil
}

// teardown takes sess down and waits until it is.
func (s *Service) teardown(sess *session) {
	s.mu.Lock()
	if sess.grace != nil {
		sess.grace.Stop()
		sess.grace = nil
	}
	sess.stopping = true
	s.mu.Unlock()
	switch sess.kind {
	case "tun":
		sess.cancel()
		<-sess.done
	case "split":
		if err := s.d.DisableSplit(); err != nil {
			slog.Warn("служба: раздельная маршрутизация снята не полностью", "error", err)
		}
		s.mu.Lock()
		if s.sess == sess {
			s.sess = nil
			s.attached()
		}
		s.mu.Unlock()
	}
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

	s.mu.Lock()
	cur := s.sess
	s.mu.Unlock()
	if m.Type == ipc.TypeRefreshSplit {
		if cur == nil || cur.kind != "split" || cur.conn != c {
			return &ipc.SplitReply{}, nil
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
	// The redirect lands in the core listening on the port; it has to be the
	// asking user's own, or the moved processes' traffic would go to whoever
	// took the port first.
	if owner, ok := s.d.ListenerUID(xraycfg.RedirectPort); !ok {
		return nil, fmt.Errorf("на порту %d никто не слушает — ядро интерфейса не запущено", xraycfg.RedirectPort)
	} else if c.Peer.UID != 0 && owner != c.Peer.UID {
		return nil, fmt.Errorf("порт %d слушает процесс другого пользователя (uid %d)", xraycfg.RedirectPort, owner)
	}
	sess := cur
	if cur == nil || cur.kind != "split" || cur.conn != c {
		sess = &session{kind: "split", title: "split"}
		if err := s.claim(c, sess); err != nil {
			return nil, err
		}
	}
	scan, err := s.d.EnableSplit(req.Names, uid)
	if err != nil {
		s.mu.Lock()
		if s.sess == sess {
			s.sess = nil
			s.attached()
		}
		s.mu.Unlock()
		return nil, err
	}
	sess.names = req.Names
	return &ipc.SplitReply{Token: sess.token, Matched: scan.Matched, Unclosed: scan.Unclosed, Moved: scan.Moved}, nil
}

// maxConns bounds a close_conns request.
const maxConns = 4096

// closeConns closes connections of the split's owner that predate the move
// of their process; each is checked to be the owner's socket (system.CloseConns).
func (s *Service) closeConns(c *ipc.Conn, m ipc.Message) (*ipc.CloseConnsReply, error) {
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil || sess.kind != "split" || sess.conn != c {
		return nil, errors.New("нет раздельной маршрутизации этого подключения")
	}
	var req ipc.CloseConns
	if err := json.Unmarshal(m.Body, &req); err != nil {
		return nil, fmt.Errorf("запрос не разобран: %w", err)
	}
	if len(req.Conns) > maxConns {
		return nil, errors.New("слишком много соединений в запросе")
	}
	uid := c.Peer.UID
	if uid == 0 {
		uid = -1
	}
	return &ipc.CloseConnsReply{Open: s.d.CloseConns(req.Conns, uid)}, nil
}

func (s *Service) disableSplit(c *ipc.Conn) error {
	s.mu.Lock()
	sess := s.sess
	s.mu.Unlock()
	if sess == nil || sess.kind != "split" {
		return nil
	}
	if sess.conn != c {
		return errors.New("сессия не этого подключения")
	}
	s.teardown(sess)
	return nil
}
