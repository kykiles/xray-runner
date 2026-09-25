//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
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

// A local elevated user gets through and is identified by SID; several
// clients in a row each get an instance.
func TestPipeListenerAccepts(t *testing.T) {
	requireElevated(t)
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
// which is what keeps it from being taken over while the service runs. The
// refusal says who holds the name.
func TestPipeListenerIsFirstInstance(t *testing.T) {
	testPipe(t)
	l, err := Listen()
	if err == nil {
		_ = l.Close()
		t.Fatal("a second listener took the same pipe name")
	}
	if want := fmt.Sprintf("процесс %d ", os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name the holder (%q): %v", want, err)
	}
}

// A logged-in user outside the group is turned away, and told how to get in.
func TestPipeRefusesOutsider(t *testing.T) {
	l := testPipe(t)
	testGroup(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Errorf("an outsider was accepted as %s", c.Peer.Name)
			c.Close()
		}
	}()
	tok := clientToken(t, false, windows.SecurityImpersonation)
	iu, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		t.Fatal(err)
	}
	interactive, _ := tok.IsMember(iu)

	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.SetThreadToken(nil, tok); err != nil {
			errc <- err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f, err := dial(ctx)
		if rerr := windows.RevertToSelf(); rerr != nil {
			panic(rerr)
		}
		if err != nil {
			errc <- err
			return
		}
		c := NewClient(f)
		defer func() { _ = c.Close() }()
		errc <- c.Call(ctx, TypeHello, Hello{Version: Version}, &HelloReply{})
	}()
	err = <-errc
	switch {
	case err == nil:
		t.Fatal("an outsider got through")
	case !interactive:
		// The test runs outside an interactive logon: the pipe's DACL keeps it
		// out before authorize.
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("a non-interactive outsider: %v, want access denied", err)
		}
	case !strings.Contains(err.Error(), usersGroup):
		t.Errorf("the refusal does not name the group: %v", err)
	}
}

func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("нужны права администратора")
	}
}

// testGroup makes UsersGroup a group of the test's own, created.
func testGroup(t *testing.T) {
	t.Helper()
	requireElevated(t)
	old := usersGroup
	usersGroup = fmt.Sprintf("xray-runner-test-%d", os.Getpid())
	t.Cleanup(func() {
		_ = DeleteUsersGroup()
		usersGroup = old
	})
	if err := CreateUsersGroup(); err != nil {
		t.Fatal(err)
	}
	if err := CreateUsersGroup(); err != nil {
		t.Fatalf("creating the group again: %v", err)
	}
}

// clientToken is this process's token as authorize reads a client's: an
// impersonation token. Without the administrators it is the token of a user
// who did not elevate (the group is there deny-only).
func clientToken(t *testing.T, admins bool, level uint32) windows.Token {
	t.Helper()
	var proc windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &proc); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Close() }()
	src := proc
	if !admins {
		ba, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if err != nil {
			t.Fatal(err)
		}
		disable := windows.SIDAndAttributes{Sid: ba}
		var restricted windows.Token
		r, _, err := procCreateRestrictedToken.Call(uintptr(proc), 0, 1, uintptr(unsafe.Pointer(&disable)),
			0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
		if r == 0 {
			t.Fatalf("CreateRestrictedToken: %v", err)
		}
		defer func() { _ = restricted.Close() }()
		src = restricted
	}
	var imp windows.Token
	if err := windows.DuplicateTokenEx(src, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, nil,
		level, windows.TokenImpersonation, &imp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imp.Close() })
	return imp
}

var procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")

// Being logged in is not enough: without elevation a user needs the group,
// and one just added gets in without logging in again. An elevated
// administrator needs no group.
func TestAdmittedByGroup(t *testing.T) {
	testGroup(t)
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	user := tu.User.Sid

	if !admitted(clientToken(t, true, windows.SecurityIdentification), user) {
		t.Error("an elevated administrator was refused")
	}
	plain := clientToken(t, false, windows.SecurityIdentification)
	if admitted(plain, user) {
		t.Fatal("a user outside the group was let in")
	}
	if err := AddToUsersGroup(user); err != nil {
		t.Fatal(err)
	}
	if err := AddToUsersGroup(user); err != nil {
		t.Fatalf("adding the member again: %v", err)
	}
	if !admitted(plain, user) {
		t.Error("a member added after logon was refused")
	}
	if err := DeleteUsersGroup(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteUsersGroup(); err != nil {
		t.Fatalf("deleting the group again: %v", err)
	}
	if admitted(plain, user) {
		t.Error("a user was let in with the group gone")
	}
}

// Only a group of this computer's own counts.
func TestUsersGroupSIDIsLocal(t *testing.T) {
	testGroup(t)
	sid := usersGroupSID()
	if sid == nil {
		t.Fatal("the group made by the test was not found")
	}
	name := usersGroup
	usersGroup = name + "-missing"
	defer func() { usersGroup = name }()
	if usersGroupSID() != nil {
		t.Error("a missing group resolved")
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
