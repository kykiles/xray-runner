//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PipeName is where the service listens.
const PipeName = `\\.\pipe\xray-runner`

// pipeName is PipeName, a variable so tests can listen elsewhere.
var pipeName = PipeName

// pipeSDDL lets SYSTEM and the administrators do anything with the pipe, and
// an interactive user read and write it — but not create an instance of it
// (FILE_CREATE_PIPE_INSTANCE, the FILE_APPEND_DATA bit left out of 0x12019b):
// with that right a user could stand up a second server under the service's
// name and take the next client. Network logons are not interactive, and the
// pipe refuses remote clients besides.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x12019b;;;IU)"

// clientAccess is what the client asks for: read and write the data, read the
// security descriptor (FILE_GENERIC_READ carries READ_CONTROL) — and nothing
// that pipeSDDL does not give an interactive user.
const clientAccess = windows.FILE_GENERIC_READ | windows.FILE_WRITE_DATA

// trustedOwner reports whether the pipe's owner is the service's kind:
// SYSTEM or the administrators. A user who created the name first cannot
// make either the owner. A variable so tests, which own their pipe, can pass.
var trustedOwner = func(owner *windows.SID) bool {
	return owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

func dial(ctx context.Context) (io.ReadWriteCloser, error) {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, err
	}
	for {
		h, err := windows.CreateFile(name, clientAccess, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			if err := checkOwner(h); err != nil {
				_ = windows.CloseHandle(h)
				return nil, err
			}
			return os.NewFile(uintptr(h), pipeName), nil
		}
		// Every instance taken: the service creates the next one at once.
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func checkOwner(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("владелец канала %s не прочитан: %w", pipeName, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trustedOwner(owner) {
		return fmt.Errorf("канал %s создан не службой (владелец %s)", pipeName, owner)
	}
	return nil
}

var (
	advapi32                       = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = advapi32.NewProc("ImpersonateNamedPipeClient")
)

func impersonateNamedPipeClient(h windows.Handle) error {
	r, _, err := procImpersonateNamedPipeClient.Call(uintptr(h))
	if r == 0 {
		return err
	}
	return nil
}

// pipeListener is the service's pipe. One instance always waits for the next
// client, so the name never goes free for someone else to take.
type pipeListener struct {
	mu      sync.Mutex
	pending windows.Handle
	closed  bool
	sa      *windows.SecurityAttributes
}

// Listen creates the service's pipe. It fails when the name is already taken:
// the first instance insists on being first.
func Listen() (Listener, error) {
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		return nil, err
	}
	l := &pipeListener{sa: &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}}
	h, err := l.instance(true)
	if err != nil {
		return nil, fmt.Errorf("канал %s: %w", pipeName, err)
	}
	l.pending = h
	return l, nil
}

func (l *pipeListener) instance(first bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	mode := uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	return windows.CreateNamedPipe(name, flags, mode, windows.PIPE_UNLIMITED_INSTANCES, 64<<10, 64<<10, 0, l.sa)
}

func (l *pipeListener) Accept() (*Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, ErrListenerClosed
		}
		h := l.pending
		l.mu.Unlock()

		if err := connectPipe(h); err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return nil, ErrListenerClosed
			}
			// A client that came and went before the connect finished leaves
			// the instance broken: replace it.
			_ = windows.DisconnectNamedPipe(h)
			continue
		}

		next, err := l.instance(false)
		l.mu.Lock()
		if err != nil || l.closed {
			l.mu.Unlock()
			_ = windows.CloseHandle(h)
			if err == nil {
				_ = windows.CloseHandle(next)
				return nil, ErrListenerClosed
			}
			return nil, err
		}
		l.pending = next
		l.mu.Unlock()

		f := os.NewFile(uintptr(h), pipeName)
		peer, why := authorize(h)
		if why != "" {
			Refuse(f, why)
			continue
		}
		return NewConn(f, peer), nil
	}
}

// connectPipe waits for a client on instance h.
func connectPipe(h windows.Handle) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(ev) }()
	ov := windows.Overlapped{HEvent: ev}
	err = windows.ConnectNamedPipe(h, &ov)
	switch {
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		return nil
	case errors.Is(err, windows.ERROR_IO_PENDING):
		var n uint32
		return windows.GetOverlappedResult(h, &ov, &n, true)
	default:
		return err
	}
}

// authorize reads the client's token by impersonating it for a moment, on a
// thread of its own: an impersonating thread must not run anything else.
func authorize(h windows.Handle) (Peer, string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateNamedPipeClient(h); err != nil {
		return Peer{}, "не удалось проверить клиента: " + err.Error()
	}
	var tok windows.Token
	err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok)
	if rerr := windows.RevertToSelf(); rerr != nil {
		// A thread left impersonating must not return to the scheduler.
		panic(rerr)
	}
	if err != nil {
		return Peer{}, "токен клиента не прочитан: " + err.Error()
	}
	defer func() { _ = tok.Close() }()

	tu, err := tok.GetTokenUser()
	if err != nil {
		return Peer{}, "токен клиента не прочитан: " + err.Error()
	}
	sid := tu.User.Sid
	peer := Peer{Key: sid.String(), UID: -1, Name: sid.String()}
	if account, domain, _, err := sid.LookupAccount(""); err == nil {
		peer.Name = domain + `\` + account
	}
	for _, wk := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid, windows.WinInteractiveSid} {
		want, err := windows.CreateWellKnownSid(wk)
		if err != nil {
			continue
		}
		if ok, err := tok.IsMember(want); err == nil && ok {
			return peer, ""
		}
	}
	return peer, "службой пользуются только пользователи, вошедшие в систему на этом компьютере"
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	_ = windows.CancelIoEx(l.pending, nil)
	return windows.CloseHandle(l.pending)
}
