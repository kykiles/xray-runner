//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
// pipe refuses remote clients besides. Whether an interactive user may use the
// service is for authorize to say (UsersGroup); the pipe lets them in so they
// hear why not.
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
			return newPipeConn(h), nil
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
// client, so the name never goes free for someone else to take. Clients are
// connected on a goroutine of its own and checked each on another: a slow
// check — an account name from the domain controller — holds up no one else.
type pipeListener struct {
	mu      sync.Mutex
	pending windows.Handle
	closed  bool
	sa      *windows.SecurityAttributes

	conns chan *Conn
	done  chan struct{} // closed with the listener
	err   error         // why the loop stopped, before done closes
}

// Listen creates the service's pipe. It fails when the name is already taken:
// the first instance insists on being first. The error says who holds it.
func Listen() (Listener, error) {
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		return nil, err
	}
	l := &pipeListener{
		sa: &windows.SecurityAttributes{
			Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
			SecurityDescriptor: sd,
		},
		conns: make(chan *Conn),
		done:  make(chan struct{}),
	}
	h, err := l.instance(true)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
		// Clients refuse a pipe of someone else's, so the service is out of
		// reach until whoever holds the name lets it go.
		return nil, fmt.Errorf("канал %s уже занят (%s); служба не примет подключений, пока его не освободят: %w",
			pipeName, pipeHolder(), err)
	}
	if err != nil {
		return nil, fmt.Errorf("канал %s: %w", pipeName, err)
	}
	l.pending = h
	go l.loop()
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
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		if l.err != nil {
			return nil, l.err
		}
		return nil, ErrListenerClosed
	}
}

// loop connects clients until the listener closes or the next instance
// cannot be made.
func (l *pipeListener) loop() {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		h := l.pending
		l.mu.Unlock()

		if err := connectPipe(h); err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return
			}
			// A client that came and went before the connect finished leaves
			// the instance broken: replace it.
			_ = windows.DisconnectNamedPipe(h)
			continue
		}

		next, err := l.instance(false)
		l.mu.Lock()
		if l.closed {
			// Close took h down with it: h is still the pending instance.
			l.mu.Unlock()
			if err == nil {
				_ = windows.CloseHandle(next)
			}
			return
		}
		if err != nil {
			l.err = err
			l.closed = true
			close(l.done)
			l.mu.Unlock()
			_ = windows.CloseHandle(h)
			return
		}
		l.pending = next
		l.mu.Unlock()

		go l.admit(h)
	}
}

// admit checks the client on instance h and hands it to Accept, or turns it
// away.
func (l *pipeListener) admit(h windows.Handle) {
	f := newPipeConn(h)
	peer, why := authorize(h)
	if why != "" {
		Refuse(f, why)
		return
	}
	c := NewConn(f, peer)
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
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
	peer := Peer{Key: sid.String(), UID: -1, Name: accountName(sid)}
	if admitted(tok, sid) {
		return peer, ""
	}
	return peer, fmt.Sprintf("службой пользуются администраторы и члены группы «%s»; "+
		"администратор добавит вас командой: net localgroup \"%s\" \"%s\" /add", usersGroup, usersGroup, peer.Name)
}

// admitted reports whether the client with token tok, user by SID, may use
// the service: SYSTEM, an elevated administrator, or a member of
// UsersGroup — through its token or, added since it logged in, the group's
// own list. Being logged in at the console is not enough: TUN and the kill
// switch are the whole machine's, and so is the core's log.
func admitted(tok windows.Token, user *windows.SID) bool {
	for _, wk := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		want, err := windows.CreateWellKnownSid(wk)
		if err != nil {
			continue
		}
		if ok, err := tok.IsMember(want); err == nil && ok {
			return true
		}
	}
	group := usersGroupSID()
	if group == nil {
		return false
	}
	if ok, err := tok.IsMember(group); err == nil && ok {
		return true
	}
	return directMember(user)
}

// accountName is DOMAIN\name for the log, the SID itself when it does not
// resolve.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	return domain + `\` + account
}

// pipeHolder says who holds the pipe's name: the owner and, if the system
// tells, the process that serves it. The pipe is opened as the client opens
// it, for identification only: whoever holds it cannot act as the service.
func pipeHolder() string {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return err.Error()
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, 0, nil, windows.OPEN_EXISTING,
		windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return "кто его держит, не узнать: " + err.Error()
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var parts []string
	if sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION); err == nil {
		if owner, _, err := sd.Owner(); err == nil {
			parts = append(parts, "владелец "+accountName(owner))
		}
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err == nil {
		parts = append(parts, fmt.Sprintf("процесс %d %s", pid, processImage(pid)))
	}
	if len(parts) == 0 {
		return "кто его держит, не узнать"
	}
	return strings.Join(parts, ", ")
}

// processImage is the executable of process pid, "" if it cannot be read.
func processImage(pid uint32) string {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(p) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func (l *pipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.done)
	_ = windows.CancelIoEx(l.pending, nil)
	return windows.CloseHandle(l.pending)
}

// pipeConn is one end of a pipe instance opened for overlapped I/O. A read
// and a write may be under way at once, each on its own OVERLAPPED — which
// is what a synchronous handle does not allow, and what os.File, keeping a
// file offset, does not expect of a handle.
type pipeConn struct {
	h       windows.Handle
	mu      sync.RWMutex // held for reading by each operation, for writing by Close
	closing atomic.Bool
	closed  bool
}

func newPipeConn(h windows.Handle) *pipeConn { return &pipeConn{h: h} }

func (p *pipeConn) Read(b []byte) (int, error) {
	n, err := p.do(b, false)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return n, io.EOF
	}
	return n, err
}

func (p *pipeConn) Write(b []byte) (int, error) {
	written := 0
	for written < len(b) {
		n, err := p.do(b[written:], true)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (p *pipeConn) do(b []byte, write bool) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed || p.closing.Load() {
		return 0, os.ErrClosed
	}
	if len(b) == 0 {
		return 0, nil
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(ev) }()
	ov := windows.Overlapped{HEvent: ev}
	var n uint32
	if write {
		err = windows.WriteFile(p.h, b, &n, &ov)
	} else {
		err = windows.ReadFile(p.h, b, &n, &ov)
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(p.h, &ov, &n, true)
	}
	if err != nil && p.closing.Load() {
		err = os.ErrClosed
	}
	return int(n), err
}

// Close cancels what is under way and closes the handle once nothing uses
// it. An operation that slipped in between the flag and the cancel is
// cancelled on the next round.
func (p *pipeConn) Close() error {
	if p.closing.Swap(true) {
		return nil
	}
	for !p.mu.TryLock() {
		_ = windows.CancelIoEx(p.h, nil)
		time.Sleep(time.Millisecond)
	}
	defer p.mu.Unlock()
	p.closed = true
	return windows.CloseHandle(p.h)
}
