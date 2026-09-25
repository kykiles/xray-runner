//go:build linux

package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testListener(t *testing.T, gid int) *unixListener {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sock")
	l, err := listenAt(path, gid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	old, oldUID := socketPath, serverUID
	socketPath, serverUID = path, os.Getuid()
	t.Cleanup(func() { socketPath, serverUID = old, oldUID })
	return l
}

// A member of the group gets through, identified by uid.
func TestUnixListenerAcceptsGroupMember(t *testing.T) {
	l := testListener(t, os.Getgid())
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		m, _ := c.Read()
		c.Reply(m.ID, HelloReply{Version: Version}, nil)
		if c.Peer.UID != os.Getuid() {
			c.Reply(99, nil, errors.New("wrong uid"))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

// Neither root nor in the group: turned away with the reason, before any
// request is read.
func TestUnixListenerRefusesOutsider(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root is always let in")
	}
	l := testListener(t, -1)
	go func() { _, _ = l.Accept() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Connect(ctx, "")
	var re *RemoteError
	if !errors.Is(err, ErrUnavailable) || !errors.As(err, &re) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// A socket held by some other uid than the service's is not dialled.
func TestDialRefusesForeignServer(t *testing.T) {
	testListener(t, os.Getgid())
	serverUID = os.Getuid() + 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dial(ctx); err == nil {
		t.Fatal("dial trusted a socket held by another uid")
	}
}

// A peer turned away hears why even when its hello comes late: the refusal
// answers the hello instead of racing it.
func TestRefusalAnswersLateHello(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root is always let in")
	}
	l := testListener(t, -1)
	go func() { _, _ = l.Accept() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rw, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rw.Close() }()
	time.Sleep(200 * time.Millisecond)
	if err := WriteMessage(rw, Message{ID: 1, Type: TypeHello, Body: []byte(`{"version":1}`)}); err != nil {
		t.Fatalf("hello not sent: %v", err)
	}
	m, err := ReadMessage(rw)
	if err != nil || m.ID != 1 || !strings.Contains(m.Error, Group) {
		t.Fatalf("reply %+v, %v", m, err)
	}
}

// Over the real socket: a client that sends and stops reading fills the
// socket's buffer; the write deadline drops it, and Close frees the handler
// without waiting for that.
func TestUnixConnClientThatDoesNotRead(t *testing.T) {
	for _, name := range []string{"deadline", "close"} {
		t.Run(name, func(t *testing.T) {
			if name == "deadline" {
				setFor(t, &writeWait, 200*time.Millisecond)
			}
			l := testListener(t, os.Getgid())
			accepted := make(chan *Conn, 1)
			go func() {
				if c, err := l.Accept(); err == nil {
					accepted <- c
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rw, err := dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rw.Close() }()
			c := <-accepted
			handled := make(chan struct{})
			go func() {
				serveLike(c)
				close(handled)
			}()
			go func() {
				for id := uint64(1); ; id++ {
					if WriteMessage(rw, Message{ID: id, Type: TypeStatus}) != nil {
						return
					}
				}
			}()
			if name == "close" {
				time.Sleep(300 * time.Millisecond)
				c.Close()
			}
			select {
			case <-handled:
			case <-time.After(5 * time.Second):
				t.Fatal("the handler is held by a client that does not read")
			}
			select {
			case <-c.wrote:
			case <-time.After(5 * time.Second):
				t.Fatal("the writer is held by a client that does not read")
			}
		})
	}
}

// A peer that connects and says nothing, or announces a large hello, is let
// go without holding a handler.
func TestUnixConnHello(t *testing.T) {
	setFor(t, &helloWait, 200*time.Millisecond)
	l := testListener(t, os.Getgid())
	var wg sync.WaitGroup
	defer wg.Wait()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { serveLike(c) })
		}
	}()
	for _, send := range [][]byte{nil, {0, 0x40, 0, 0}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rw, err := dial(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if send != nil {
			_, _ = rw.Write(send)
		}
		_ = rw.(*net.UnixConn).SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := ReadMessage(rw); !errors.Is(err, io.EOF) {
			t.Fatalf("hello %v: err = %v, want the connection closed", send, err)
		}
		_ = rw.Close()
	}
}
