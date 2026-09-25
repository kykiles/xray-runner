package ipc

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is who is on the other end of a connection, as the operating system
// vouches for it — SO_PEERCRED on Linux, the pipe client's token on Windows.
type Peer struct {
	// Key identifies the user across connections: "uid:1000", or the SID.
	Key string
	// UID is the Linux user id, -1 on Windows.
	UID int
	// Name is for the log.
	Name string
}

// Listener hands out connections from peers allowed to use the service; the
// others are turned away before Accept returns.
type Listener interface {
	Accept() (*Conn, error)
	Close() error
}

// ErrListenerClosed is Accept on a closed listener.
var ErrListenerClosed = errors.New("ipc: listener closed")

// Conn is the service's end of one connection. Writes go through a queue of
// their own, so a client that stops reading holds up only itself: its log
// lines are dropped, anything else waits for it alone, and a write it leaves
// hanging for writeWait drops it.
type Conn struct {
	rw   io.ReadWriteCloser
	Peer Peer

	out       chan Message
	closed    chan struct{}
	closeOnce sync.Once
	wrote     chan struct{}
	released  func() // called once rw is closed; nil for none
	greeted   bool   // the first message is read; the reader's own
}

const outBuffer = 256

// A deadliner is a connection whose reads and writes can be given a time to
// end by: net.UnixConn, the Windows pipe, net.Pipe in tests. Without one a
// connection still goes on Close, only a stuck write takes that long.
type deadliner interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

var (
	// writeWait is how long one message may take to go out before the
	// client is taken for one that stopped reading, and dropped.
	writeWait = 10 * time.Second
	// drainWait is how long Close lets what is queued go out.
	drainWait = 300 * time.Millisecond
	// helloWait is how long the first message may take to arrive.
	helloWait = 5 * time.Second
)

// MaxHello bounds the first message on a connection, the one read before the
// peer has said anything that makes it worth more: a hello is some hundred
// bytes.
const MaxHello = 4 << 10

// NewConn serves the protocol over an accepted connection.
func NewConn(rw io.ReadWriteCloser, peer Peer) *Conn { return newConn(rw, peer, nil) }

func newConn(rw io.ReadWriteCloser, peer Peer, released func()) *Conn {
	c := &Conn{
		rw:       rw,
		Peer:     peer,
		out:      make(chan Message, outBuffer),
		closed:   make(chan struct{}),
		wrote:    make(chan struct{}),
		released: released,
	}
	go c.writeLoop()
	return c
}

func (c *Conn) writeLoop() {
	defer close(c.wrote)
	for {
		select {
		case m := <-c.out:
			if err := c.write(m); err != nil {
				c.Close()
				return
			}
		case <-c.closed:
			// What was queued before the close still goes out, as far as
			// drainWait lets it: a refusal is sent and then the connection
			// closed.
			for {
				select {
				case m := <-c.out:
					if c.write(m) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (c *Conn) write(m Message) error {
	if d, ok := c.rw.(deadliner); ok {
		_ = d.SetWriteDeadline(time.Now().Add(writeWait))
	}
	return WriteMessage(c.rw, m)
}

// Read takes the next request. The first one, the hello, is held to MaxHello
// and must come within helloWait: until then the peer is only somebody who
// may connect.
func (c *Conn) Read() (Message, error) {
	if c.greeted {
		return ReadMessage(c.rw)
	}
	c.greeted = true
	d, ok := c.rw.(deadliner)
	if ok {
		_ = d.SetReadDeadline(time.Now().Add(helloWait))
	}
	m, err := readMessage(c.rw, MaxHello)
	if ok && err == nil {
		_ = d.SetReadDeadline(time.Time{})
	}
	return m, err
}

// Reply answers request id: with body, or with err when it is not nil.
func (c *Conn) Reply(id uint64, body any, err error) {
	m := Message{ID: id, Type: TypeReply}
	if err != nil {
		m.Error = err.Error()
	} else if body != nil {
		data, merr := json.Marshal(body)
		if merr != nil {
			m.Error = merr.Error()
		} else {
			m.Body = data
		}
	}
	c.send(m, false)
}

// Event sends ev unasked. A log line is dropped when the client lags behind.
func (c *Conn) Event(ev Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	c.send(Message{Type: TypeEvent, Body: data}, ev.Kind == EventLog)
}

func (c *Conn) send(m Message, droppable bool) {
	if droppable {
		select {
		case c.out <- m:
		case <-c.closed:
		default:
		}
		return
	}
	select {
	case c.out <- m:
	case <-c.closed:
	}
}

// Close drops the connection once what is queued has gone out, or after
// drainWait when it does not: a client that does not read gets no longer.
// It returns at once.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		go func() {
			t := time.NewTimer(drainWait)
			select {
			case <-c.wrote:
			case <-t.C:
			}
			t.Stop()
			_ = c.rw.Close()
			if c.released != nil {
				c.released()
			}
		}()
	})
}

// Closed is closed with the connection.
func (c *Conn) Closed() <-chan struct{} { return c.closed }

// Refuse tells a peer why it is turned away and closes the connection. The
// refusal answers the peer's hello, read first: a connection closed before
// the client got its hello out fails that write, and the client would never
// read why. A peer that says nothing gets its answer after a moment anyway,
// and the connection is gone after twice that whatever the peer does. The
// hello is held to MaxHello. It returns at once; the waiting is done on the
// side.
func Refuse(rw io.ReadWriteCloser, why string) {
	slog.Warn("ipc: подключение отклонено", "reason", why)
	go func() {
		// Close unblocks the read below, and a write to a peer that does not
		// read.
		t := time.AfterFunc(2*refuseWait, func() { _ = rw.Close() })
		defer func() {
			t.Stop()
			_ = rw.Close()
		}()
		hello := make(chan uint64, 1)
		go func() {
			if m, err := readMessage(rw, MaxHello); err == nil {
				hello <- m.ID
			}
		}()
		id := uint64(1) // the client's first call is its hello
		wait := time.NewTimer(refuseWait)
		select {
		case id = <-hello:
		case <-wait.C:
		}
		wait.Stop()
		_ = WriteMessage(rw, Message{ID: id, Type: TypeReply, Error: why})
	}()
}

// refuseWait is how long Refuse waits for the hello it answers.
const refuseWait = 2 * time.Second

// MaxConns bounds the connections the service serves at once. Past it a new
// one is refused: a client that opens connections and says nothing on them
// ties up no more than this.
const MaxConns = 64

// connLimit counts the connections it let in that are not closed yet.
type connLimit struct {
	live atomic.Int32
	max  int32
}

// conns is the service's: there is one listener to a process.
var conns = &connLimit{max: MaxConns}

// admit serves an authorized connection, or refuses it when max are open
// already; nil then.
func (l *connLimit) admit(rw io.ReadWriteCloser, peer Peer) *Conn {
	if l.live.Add(1) > l.max {
		l.live.Add(-1)
		Refuse(rw, "к службе открыто слишком много подключений, повторите позже")
		return nil
	}
	return newConn(rw, peer, func() { l.live.Add(-1) })
}
