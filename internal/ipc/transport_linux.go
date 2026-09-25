//go:build linux

package ipc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"slices"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// SocketPath is where the service listens. /run/xray-runner is root's: nobody
// else can put a socket there to pass for the service.
const SocketPath = "/run/xray-runner/control.sock"

// Group is the group whose members may use the service, next to root.
const Group = "xray-runner"

// socketPath is SocketPath, a variable so tests can listen elsewhere.
var socketPath = SocketPath

// serverUID is the uid the client expects the service to run as; a variable
// so tests need not be root.
var serverUID = 0

func dial(ctx context.Context) (io.ReadWriteCloser, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	uc := c.(*net.UnixConn)
	// The socket's directory is root's, so this should always hold; a service
	// that is not root is not the one this client means to trust.
	cred, err := peerCred(uc)
	if err != nil {
		_ = uc.Close()
		return nil, err
	}
	if int(cred.Uid) != serverUID {
		_ = uc.Close()
		return nil, fmt.Errorf("сокет %s держит не root (uid %d)", socketPath, cred.Uid)
	}
	return uc, nil
}

func peerCred(c *net.UnixConn) (*unix.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, cerr
}

// peerGroups is the peer's supplementary groups at connect time
// (SO_PEERGROUPS, Linux 4.13+), as far as 64 of them go; more fail with
// ERANGE, and the group database answers instead.
func peerGroups(c *net.UnixConn) ([]uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var data string
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		data, gerr = unix.GetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_PEERGROUPS)
	}); err != nil {
		return nil, err
	}
	if gerr != nil {
		return nil, gerr
	}
	groups := make([]uint32, 0, len(data)/4)
	for i := 0; i+4 <= len(data); i += 4 {
		groups = append(groups, binary.NativeEndian.Uint32([]byte(data[i:i+4])))
	}
	return groups, nil
}

// unixListener is the service's socket.
type unixListener struct {
	l   *net.UnixListener
	gid int // the group allowed next to root; -1 when there is none
}

// Listen opens the service's socket: the one systemd passed in on socket
// activation, or its own at SocketPath, reachable by root and the group only.
func Listen() (Listener, error) {
	gid := -1
	if g, err := user.LookupGroup(Group); err == nil {
		gid, _ = strconv.Atoi(g.Gid)
	}
	l, err := activated()
	if err != nil {
		return nil, err
	}
	// Under systemd too: DirectoryMode only holds for a directory the socket
	// unit creates, and a run under sudo earlier in the boot may have left it
	// 0700. The unit lets the service write there, and the directory is
	// root's, so the service needs no CAP_FOWNER to change its mode.
	if err := RuntimeDir(); err != nil {
		if l != nil {
			_ = l.Close()
		}
		return nil, err
	}
	if l != nil {
		return &unixListener{l: l, gid: gid}, nil
	}
	_ = os.Remove(socketPath)
	l, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o600)
	if gid >= 0 {
		if err := os.Chown(socketPath, 0, gid); err == nil {
			mode = 0o660
		}
	}
	if err := os.Chmod(socketPath, mode); err != nil {
		_ = l.Close()
		return nil, err
	}
	return &unixListener{l: l, gid: gid}, nil
}

// runtimeDir holds the socket, and root's journal records (package system);
// a variable so tests can have it elsewhere.
var runtimeDir = "/run/xray-runner"

// RuntimeDir makes the socket's directory passable for the group: the journal
// creates it 0700 when it gets there first, and a member of the group could
// then not reach the socket inside. What else is in it is root's own 0600.
func RuntimeDir() error {
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil { //nolint:gosec // G301: passable, not listable content — see above
		return err
	}
	return os.Chmod(runtimeDir, 0o755) //nolint:gosec // G302: as above
}

// listenAt is Listen at a path of the test's choosing, open to the given gid.
func listenAt(path string, gid int) (*unixListener, error) {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return &unixListener{l: l, gid: gid}, nil
}

// listenFDsStart is the first descriptor systemd passes (SD_LISTEN_FDS_START),
// a variable so tests can pass one of their own.
var listenFDsStart = 3

// activated is the socket systemd passed in (sd_listen_fds), nil without one.
func activated() (*net.UnixListener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n < 1 {
		return nil, nil
	}
	_ = os.Unsetenv("LISTEN_PID")
	_ = os.Unsetenv("LISTEN_FDS")
	_ = os.Unsetenv("LISTEN_FDNAMES")
	first := listenFDsStart
	syscall.CloseOnExec(first)
	f := os.NewFile(uintptr(first), "systemd-socket")
	l, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("сокет от systemd: %w", err)
	}
	ul, ok := l.(*net.UnixListener)
	if !ok {
		_ = l.Close()
		return nil, errors.New("сокет от systemd не unix-сокет")
	}
	return ul, nil
}

func (u *unixListener) Accept() (*Conn, error) {
	for {
		c, err := u.l.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil, ErrListenerClosed
			}
			return nil, err
		}
		peer, why := u.authorize(c)
		if why != "" {
			Refuse(c, why)
			continue
		}
		return NewConn(c, peer), nil
	}
}

// authorize checks the peer against root and the group. The socket's mode
// already keeps everybody else from connecting; this holds even when the
// socket came from a unit file that got the mode wrong.
func (u *unixListener) authorize(c *net.UnixConn) (Peer, string) {
	cred, err := peerCred(c)
	if err != nil {
		return Peer{}, "не удалось узнать, кто подключился: " + err.Error()
	}
	peer := Peer{Key: "uid:" + strconv.Itoa(int(cred.Uid)), UID: int(cred.Uid), Name: userName(cred.Uid)}
	if cred.Uid == 0 || u.member(c, cred) {
		return peer, ""
	}
	return peer, fmt.Sprintf("пользователь %s не в группе %s — добавьте его (sudo usermod -aG %s %s) и войдите в систему заново", peer.Name, Group, Group, peer.Name)
}

func (u *unixListener) member(c *net.UnixConn, cred *unix.Ucred) bool {
	if u.gid < 0 {
		return false
	}
	if int(cred.Gid) == u.gid {
		return true
	}
	if groups, err := peerGroups(c); err == nil {
		return slices.Contains(groups, uint32(u.gid)) //nolint:gosec // G115: a gid
	}
	// No SO_PEERGROUPS: the group database says what the user belongs to.
	usr, err := user.LookupId(strconv.Itoa(int(cred.Uid)))
	if err != nil {
		return false
	}
	ids, err := usr.GroupIds()
	return err == nil && slices.Contains(ids, strconv.Itoa(u.gid))
}

func userName(uid uint32) string {
	if u, err := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		return u.Username
	}
	return strconv.Itoa(int(uid))
}

func (u *unixListener) Close() error { return u.l.Close() }
