package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// setFor sets *p to v for the test.
func setFor[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// A length is a promise, not something to set memory aside for: a frame
// announced large and cut short costs what arrived.
func TestReadMessageGrowsWithData(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxMessage)
	in := append(hdr[:], `{"v":1`...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := ReadMessage(bytes.NewReader(in))
	runtime.ReadMemStats(&after)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want ErrUnexpectedEOF", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > MaxMessage/4 {
		t.Fatalf("a cut frame allocated %d bytes", got)
	}
}

// The first message is held to MaxHello; those after it are not.
func TestConnHelloLimit(t *testing.T) {
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	c := NewConn(srv, Peer{Key: "k"})
	defer c.Close()
	go func() {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], MaxHello+1)
		_, _ = cli.Write(hdr[:])
	}()
	if _, err := c.Read(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized hello: err = %v, want ErrTooLarge", err)
	}

	cli, srv = net.Pipe()
	defer func() { _ = cli.Close() }()
	c = NewConn(srv, Peer{Key: "k"})
	defer c.Close()
	big := `"` + strings.Repeat("a", 2*MaxHello) + `"`
	go func() {
		_ = WriteMessage(cli, Message{ID: 1, Type: TypeHello, Body: []byte(`{"version":1}`)})
		_ = WriteMessage(cli, Message{ID: 2, Type: TypeStartTun, Body: []byte(big)})
	}()
	if m, err := c.Read(); err != nil || m.Type != TypeHello {
		t.Fatalf("hello: %+v, %v", m, err)
	}
	if m, err := c.Read(); err != nil || string(m.Body) != big {
		t.Fatalf("request after the hello: %v", err)
	}
}

// A peer that connects and says nothing is let go after helloWait.
func TestConnHelloTimeout(t *testing.T) {
	setFor(t, &helloWait, 100*time.Millisecond)
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	c := NewConn(srv, Peer{Key: "k"})
	defer c.Close()
	done := make(chan error, 1)
	go func() {
		_, err := c.Read()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("err = %v, want a deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent peer holds the first read")
	}
}

// serveLike is the service's handler in small: a reply to every request,
// until the connection goes.
func serveLike(c *Conn) {
	defer c.Close()
	for {
		m, err := c.Read()
		if err != nil {
			return
		}
		c.Reply(m.ID, StatusReply{Title: strings.Repeat("s", 1024)}, nil)
	}
}

// A client that sends and does not read leaves the handler stuck on a full
// queue and the writer on the connection; Close, as the service does on
// stopping, still frees both at once instead of waiting for the client.
func TestConnCloseFreesClientThatDoesNotRead(t *testing.T) {
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	c := NewConn(srv, Peer{Key: "k"})
	handled := make(chan struct{})
	go func() {
		serveLike(c)
		close(handled)
	}()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for id := uint64(1); ; id++ {
			if WriteMessage(cli, Message{ID: id, Type: TypeStatus}) != nil {
				return
			}
		}
	}()
	// Past the queue: the handler waits on a reply it cannot hand over.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	c.Close()
	for _, ch := range []chan struct{}{handled, c.wrote, sent} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("Close waits for a client that does not read")
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %v", d)
	}
}

// A client that stops reading is dropped once a write has hung for
// writeWait, whoever else waits on it.
func TestConnDropsClientThatDoesNotRead(t *testing.T) {
	setFor(t, &writeWait, 100*time.Millisecond)
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	c := NewConn(srv, Peer{Key: "k"})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-c.Closed():
				return
			default:
				c.Event(Event{Kind: EventStatus, Note: "n"})
			}
		}
	}()
	select {
	case <-c.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("a client that does not read is kept")
	}
	wg.Wait()
}

// A refused peer that announces a large hello gets its refusal all the same,
// and nothing is read past the length.
func TestRefuseLargeHello(t *testing.T) {
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	Refuse(srv, "нельзя")
	go func() {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], MaxMessage)
		_, _ = cli.Write(hdr[:])
	}()
	_ = cli.SetReadDeadline(time.Now().Add(10 * time.Second))
	m, err := ReadMessage(cli)
	if err != nil || m.Error != "нельзя" {
		t.Fatalf("reply %+v, %v", m, err)
	}
}

// Past the limit a connection is refused; one closing makes room again.
func TestAdmitLimit(t *testing.T) {
	lim := &connLimit{max: 2}
	var conns []*Conn
	for range 2 {
		cli, srv := net.Pipe()
		defer func() { _ = cli.Close() }()
		c := lim.admit(srv, Peer{Key: "k"})
		if c == nil {
			t.Fatal("refused under the limit")
		}
		conns = append(conns, c)
	}
	cli, srv := net.Pipe()
	defer func() { _ = cli.Close() }()
	if c := lim.admit(srv, Peer{Key: "k"}); c != nil {
		c.Close()
		t.Fatal("admitted past the limit")
	}
	go func() { _ = WriteMessage(cli, Message{ID: 1, Type: TypeHello, Body: []byte(`{"version":1}`)}) }()
	if m, err := ReadMessage(cli); err != nil || m.ID != 1 || m.Error == "" {
		t.Fatalf("refusal %+v, %v", m, err)
	}

	conns[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for lim.live.Load() >= lim.max {
		if time.Now().After(deadline) {
			t.Fatal("a closed connection still counts")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cli, srv = net.Pipe()
	defer func() { _ = cli.Close() }()
	c := lim.admit(srv, Peer{Key: "k"})
	if c == nil {
		t.Fatal("refused after one closed")
	}
	c.Close()
	conns[1].Close()
}
