package ipc

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
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
// lines are dropped, and anything else waits for it alone.
type Conn struct {
	rw   io.ReadWriteCloser
	Peer Peer

	out       chan Message
	closed    chan struct{}
	closeOnce sync.Once
	wrote     chan struct{}
}

const outBuffer = 256

// NewConn serves the protocol over an accepted connection.
func NewConn(rw io.ReadWriteCloser, peer Peer) *Conn {
	c := &Conn{
		rw:     rw,
		Peer:   peer,
		out:    make(chan Message, outBuffer),
		closed: make(chan struct{}),
		wrote:  make(chan struct{}),
	}
	go c.writeLoop()
	return c
}

func (c *Conn) writeLoop() {
	defer close(c.wrote)
	for {
		select {
		case m := <-c.out:
			if err := WriteMessage(c.rw, m); err != nil {
				c.Close()
				return
			}
		case <-c.closed:
			// What was queued before the close still goes out: a refusal is
			// sent and then the connection closed.
			for {
				select {
				case m := <-c.out:
					if WriteMessage(c.rw, m) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// Read takes the next request.
func (c *Conn) Read() (Message, error) { return ReadMessage(c.rw) }

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

// Close drops the connection once what is queued has gone out.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		go func() {
			<-c.wrote
			_ = c.rw.Close()
		}()
	})
}

// Closed is closed with the connection.
func (c *Conn) Closed() <-chan struct{} { return c.closed }

// Refuse tells a peer why it is turned away and closes the connection. The
// refusal is a reply to a hello it has not sent yet: the client's first call
// is its hello, id 1.
func Refuse(rw io.ReadWriteCloser, why string) {
	_ = WriteMessage(rw, Message{ID: 1, Type: TypeReply, Error: why})
	_ = rw.Close()
	slog.Warn("ipc: подключение отклонено", "reason", why)
}
