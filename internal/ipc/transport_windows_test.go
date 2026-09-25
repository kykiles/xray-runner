//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func testPipe(t *testing.T) Listener {
	t.Helper()
	old, oldOwner := pipeName, trustedOwner
	pipeName = fmt.Sprintf(`\\.\pipe\xray-runner-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	trustedOwner = func(*windows.SID) bool { return true }
	t.Cleanup(func() { pipeName, trustedOwner = old, oldOwner })
	l, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// A local interactive (or elevated) user gets through and is identified by
// SID; several clients in a row each get an instance.
func TestPipeListenerAccepts(t *testing.T) {
	l := testPipe(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				m, err := c.Read()
				if err != nil {
					return
				}
				c.Reply(m.ID, HelloReply{Version: Version, ServiceVersion: c.Peer.Key}, nil)
			}()
		}
	}()
	for i := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := Connect(ctx, "")
		cancel()
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		if sid := c.Hello().ServiceVersion; len(sid) < 4 || sid[:4] != "S-1-" {
			t.Fatalf("peer key %q is not a SID", sid)
		}
		_ = c.Close()
	}
}

// The name is the first instance's: a second listener under it is refused,
// which is what keeps it from being taken over while the service runs.
func TestPipeListenerIsFirstInstance(t *testing.T) {
	testPipe(t)
	if l, err := Listen(); err == nil {
		_ = l.Close()
		t.Fatal("a second listener took the same pipe name")
	}
}

// A pipe whose owner is not trusted is not dialled.
func TestDialRefusesUntrustedOwner(t *testing.T) {
	testPipe(t)
	trustedOwner = func(*windows.SID) bool { return false }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dial(ctx); err == nil {
		t.Fatal("dial trusted a pipe of an untrusted owner")
	}
}

// pipePair is the two ends of one pipe instance, the service's and the
// client's.
func pipePair(t *testing.T) (srv, cli *pipeConn) {
	t.Helper()
	old, oldOwner := pipeName, trustedOwner
	pipeName = fmt.Sprintf(`\\.\pipe\xray-runner-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	trustedOwner = func(*windows.SID) bool { return true }
	t.Cleanup(func() { pipeName, trustedOwner = old, oldOwner })
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		t.Fatal(err)
	}
	l := &pipeListener{sa: &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}}
	h, err := l.instance(true)
	if err != nil {
		t.Fatal(err)
	}
	connected := make(chan error, 1)
	go func() { connected <- connectPipe(h) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rw, err := dial(ctx)
	if err != nil {
		_ = windows.CloseHandle(h)
		t.Fatal(err)
	}
	if err := <-connected; err != nil {
		_ = windows.CloseHandle(h)
		_ = rw.Close()
		t.Fatal(err)
	}
	srv, cli = newPipeConn(h), rw.(*pipeConn)
	t.Cleanup(func() {
		_ = srv.Close()
		_ = cli.Close()
	})
	return srv, cli
}

// within fails the test unless done is closed in time.
func within(t *testing.T, d time.Duration, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal(what)
	}
}

// Reads and writes from both ends at once, and a Close in the middle of
// them: everything under way returns, and nothing touches the handle after
// it is closed.
func TestPipeConnConcurrentReadWriteClose(t *testing.T) {
	for range 20 {
		srv, cli := pipePair(t)
		var wg sync.WaitGroup
		for _, p := range []*pipeConn{srv, cli} {
			wg.Go(func() {
				buf := make([]byte, 4096)
				for {
					if _, err := p.Read(buf); err != nil {
						return
					}
				}
			})
			wg.Go(func() {
				buf := make([]byte, 4096)
				for {
					if _, err := p.Write(buf); err != nil {
						return
					}
				}
			})
		}
		time.Sleep(20 * time.Millisecond)
		closed := make(chan struct{})
		go func() {
			_ = srv.Close()
			_ = cli.Close()
			close(closed)
		}()
		within(t, 5*time.Second, closed, "Close did not return")
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		within(t, 5*time.Second, done, "an operation outlived Close")
		if _, err := srv.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write after Close: %v", err)
		}
	}
}

// A write the other end does not read is cancelled by Close.
func TestPipeConnCloseDuringWrite(t *testing.T) {
	srv, _ := pipePair(t)
	done := make(chan struct{})
	var werr error
	go func() {
		_, werr = srv.Write(make([]byte, 1<<20)) // past the pipe's 64 KiB buffer
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	_ = srv.Close()
	within(t, 5*time.Second, done, "a write the peer does not read holds Close")
	if !errors.Is(werr, os.ErrClosed) {
		t.Fatalf("write: %v, want os.ErrClosed", werr)
	}
}

// Deadlines cancel what runs past them; the connection is fine afterwards.
func TestPipeConnDeadlines(t *testing.T) {
	srv, cli := pipePair(t)

	_ = srv.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	start := time.Now()
	n, err := srv.Write(make([]byte, 1<<20))
	if !errors.Is(err, os.ErrDeadlineExceeded) || n >= 1<<20 {
		t.Fatalf("write past its deadline: %d, %v", n, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("write deadline took %v", d)
	}
	if _, err := srv.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write after the deadline: %v", err)
	}

	_ = srv.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := srv.Read(make([]byte, 16)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past its deadline: %v", err)
	}

	// Cleared, the deadlines hold nothing up.
	_ = srv.SetReadDeadline(time.Time{})
	_ = srv.SetWriteDeadline(time.Time{})
	go func() { _ = WriteMessage(cli, Message{ID: 1, Type: TypeHello, Body: []byte(`{"version":1}`)}) }()
	m, err := ReadMessage(srv)
	if err != nil || m.ID != 1 {
		t.Fatalf("read after clearing the deadline: %+v, %v", m, err)
	}
}

// A Conn over the pipe drops a client that does not read, and Close frees
// it at once.
func TestPipeConnClientThatDoesNotRead(t *testing.T) {
	setFor(t, &writeWait, 200*time.Millisecond)
	srv, _ := pipePair(t)
	c := NewConn(srv, Peer{Key: "k"})
	go func() {
		for {
			select {
			case <-c.Closed():
				return
			default:
				c.Event(Event{Kind: EventStatus, Note: strings.Repeat("n", 4096)})
			}
		}
	}()
	within(t, 5*time.Second, c.Closed(), "a client that does not read is kept")
	within(t, 5*time.Second, c.wrote, "the writer is held by a client that does not read")
}
