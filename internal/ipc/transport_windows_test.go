//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
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
