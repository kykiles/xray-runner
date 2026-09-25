//go:build linux

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// On socket activation the socket's directory is made passable too: a run
// under sudo may have left it 0700, and systemd keeps the mode it finds.
func TestListenActivatedFixesRuntimeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "xray-runner")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sock")
	sl, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sl.Close() }()
	f, err := sl.File()
	if err != nil {
		t.Fatal(err)
	}
	// The descriptor handed over is Listen's to close, as systemd's is.
	fd, err := syscall.Dup(int(f.Fd()))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	oldDir, oldFD := runtimeDir, listenFDsStart
	runtimeDir, listenFDsStart = dir, fd
	t.Cleanup(func() { runtimeDir, listenFDsStart = oldDir, oldFD })
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")

	l, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if got := l.(*unixListener).l.Addr().String(); got != path {
		t.Fatalf("listening at %q, want the socket passed in", got)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("directory mode %v, want 0755", st.Mode().Perm())
	}
}
