//go:build windows

package ipc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
