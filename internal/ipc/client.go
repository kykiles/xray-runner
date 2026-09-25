package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ErrUnavailable wraps every failure to reach the service: there is none
// installed, it is stopped, or this user may not use it. The interface then
// works in its own process, as it did before the service existed.
var ErrUnavailable = errors.New("служба недоступна")

// ErrClosed is a call on a connection that has gone.
var ErrClosed = errors.New("соединение со службой закрыто")

// Client is the interface's end of one connection.
type Client struct {
	rw  io.ReadWriteCloser
	wmu sync.Mutex

	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan Message
	err     error

	events chan Event
	done   chan struct{}
	once   sync.Once

	hello HelloReply
}

// Hello is what the service said when the connection opened.
func (c *Client) Hello() HelloReply { return c.hello }

// eventBuffer is how many events wait for the reader. Log lines beyond it are
// dropped rather than holding up the replies behind them.
const eventBuffer = 256

// Connect reaches the service and says hello. resume names a session to take
// back (Hello.Resume), empty for none.
func Connect(ctx context.Context, resume string) (*Client, error) {
	rw, err := dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	c := NewClient(rw)
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Call(hctx, TypeHello, Hello{Version: Version, Resume: resume}, &c.hello); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return c, nil
}

// NewClient runs the protocol over an open connection; Connect is the usual
// way in, this one is for tests.
func NewClient(rw io.ReadWriteCloser) *Client {
	c := &Client{
		rw:      rw,
		pending: map[uint64]chan Message{},
		events:  make(chan Event, eventBuffer),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Events delivers what the service says unasked. It is closed with the
// connection; its reader must keep taking from it.
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed once the connection is gone.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection went, once Done is closed.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close drops the connection.
func (c *Client) Close() error {
	err := c.rw.Close()
	c.finish(ErrClosed)
	return err
}

func (c *Client) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		pending := c.pending
		c.pending = nil
		c.mu.Unlock()
		for _, ch := range pending {
			close(ch)
		}
		close(c.done)
	})
}

func (c *Client) readLoop() {
	defer close(c.events)
	for {
		m, err := ReadMessage(c.rw)
		if err != nil {
			_ = c.rw.Close()
			if errors.Is(err, io.EOF) {
				err = ErrClosed
			}
			c.finish(err)
			return
		}
		switch m.Type {
		case TypeReply:
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m // buffered, one reply per call
			}
		case TypeEvent:
			var ev Event
			if json.Unmarshal(m.Body, &ev) != nil {
				continue
			}
			if ev.Kind == EventLog {
				select {
				case c.events <- ev:
				default:
				}
				continue
			}
			c.events <- ev
		}
	}
}

// Call sends a request and waits for its reply, decoding the reply's body
// into reply when it is not nil. A reply carrying an error is returned as
// one.
func (c *Client) Call(ctx context.Context, typ string, req, reply any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ch := make(chan Message, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()

	c.wmu.Lock()
	err = WriteMessage(c.rw, Message{ID: id, Type: typ, Body: body})
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return err
	}

	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != "" {
			return &RemoteError{Msg: m.Error}
		}
		if reply != nil && len(m.Body) > 0 {
			return json.Unmarshal(m.Body, reply)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Client) forget(id uint64) {
	c.mu.Lock()
	if c.pending != nil {
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// RemoteError is a request the service refused, in its own words.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }
