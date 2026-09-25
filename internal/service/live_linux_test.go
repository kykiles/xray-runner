//go:build linux

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"xray-runner/internal/ipc"
	"xray-runner/internal/xraycfg"
)

// The live test runs the built service with the real core against a stand-in
// VPN server, and changes the network of the namespace it runs in — so it runs
// only when asked to, in a namespace of its own, as root:
//
//	go build -o live/xray-runner ./cmd/xray-runner   # and the core in live/bin/
//	go test -c -o servicetest ./internal/service/
//	sudo env XRAY_RUNNER_NETNS=1 XRAY_RUNNER_LIVE_DIR=$PWD/live unshare -n ./servicetest -test.run Live -test.v
//
// The CI does that on every push (ci.yml).
//
// The machine it builds: an uplink live-wan (192.168.31.94/24, default via
// .1) whose far end sits in a second namespace, "beyond the gateway". There
// are the stand-in server (192.168.31.1) and the site behind it (203.0.113.10).
// Traffic from this namespace to the site reaches it through the tunnel — the
// service's core, the server — which the server's access log shows.
const (
	liveServer = "192.168.31.1"
	liveSite   = "203.0.113.10"
	liveUUID   = "5b0f3c2e-8d4a-4f1e-9c3b-7a6d2e1f0a9b"
)

func requireLive(t *testing.T) string {
	t.Helper()
	if os.Getenv("XRAY_RUNNER_NETNS") != "1" {
		t.Skip("set XRAY_RUNNER_NETNS=1 and XRAY_RUNNER_LIVE_DIR, run under `unshare -n` as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("XRAY_RUNNER_NETNS=1 needs root")
	}
	dir := os.Getenv("XRAY_RUNNER_LIVE_DIR")
	for _, f := range []string{"xray-runner", "bin/xray"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("XRAY_RUNNER_LIVE_DIR=%q: %v", dir, err)
		}
	}
	links, err := netlink.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if n := l.Attrs().Name; n != "lo" {
			t.Fatalf("interface %s: this is not a fresh network namespace", n)
		}
	}
	return dir
}

type liveNet struct {
	far        netns.NsHandle
	serverPort int
	access     string
}

// liveHost builds the two namespaces, the stand-in server and the site.
func liveHost(t *testing.T, dir string) *liveNet {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	here, err := netns.Get()
	must(t, err)
	defer func() { _ = here.Close() }()

	lo, err := netlink.LinkByName("lo")
	must(t, err)
	must(t, netlink.LinkSetUp(lo))
	must(t, netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "live-wan"}, PeerName: "live-far"}))
	wan, err := netlink.LinkByName("live-wan")
	must(t, err)
	addr(t, wan, "192.168.31.94/24")
	must(t, netlink.LinkSetUp(wan))

	far, err := netns.New() // this thread is in it from here on
	must(t, err)
	t.Cleanup(func() { _ = far.Close() })
	must(t, netns.Set(here))
	peer, err := netlink.LinkByName("live-far")
	must(t, err)
	must(t, netlink.LinkSetNsFd(peer, int(far)))
	must(t, netlink.RouteAdd(&netlink.Route{LinkIndex: wan.Attrs().Index, Gw: net.ParseIP(liveServer)}))

	must(t, netns.Set(far))
	defer func() { must(t, netns.Set(here)) }()
	flo, err := netlink.LinkByName("lo")
	must(t, err)
	must(t, netlink.LinkSetUp(flo))
	fpeer, err := netlink.LinkByName("live-far")
	must(t, err)
	addr(t, fpeer, liveServer+"/24")
	addr(t, fpeer, liveSite+"/32")
	must(t, netlink.LinkSetUp(fpeer))

	// The site: answers 204, the way the health check's probes do.
	l, err := net.Listen("tcp", liveSite+":80")
	must(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	// The stand-in server, the app's own core as a plain VLESS server.
	n := &liveNet{far: far, serverPort: 44443, access: filepath.Join(t.TempDir(), "access.log")}
	cfg, _ := json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "info", "access": n.access},
		"inbounds": []any{map[string]any{
			"listen": liveServer, "port": n.serverPort, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": liveUUID}}, "decryption": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"0.0.0.0/0"}}}}}},
	})
	cfgPath := filepath.Join(t.TempDir(), "server.json")
	must(t, os.WriteFile(cfgPath, cfg, 0o600))
	cmd := exec.Command(filepath.Join(dir, "bin", "xray"), "run", "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	must(t, cmd.Start()) // forked from this thread: in the far namespace
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitDial(t, fmt.Sprintf("%s:%d", liveServer, n.serverPort))
	return n
}

func addr(t *testing.T, l netlink.Link, cidr string) {
	t.Helper()
	a, err := netlink.ParseAddr(cidr)
	must(t, err)
	must(t, netlink.AddrAdd(l, a))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitDial(t *testing.T, hostport string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", hostport, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come up: %v", hostport, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// liveService is the built service, started the way systemd starts it: with
// the socket handed in as fd 3.
type liveService struct {
	cmd  *exec.Cmd
	sock string
	log  string
	done chan struct{}
}

func startService(t *testing.T, dir, sock string) *liveService {
	t.Helper()
	_ = os.Remove(sock)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	must(t, err)
	l.SetUnlinkOnClose(false)
	f, err := l.File()
	must(t, err)
	_ = l.Close()
	s := &liveService{sock: sock, log: filepath.Join(t.TempDir(), "service.log"), done: make(chan struct{})}
	out, err := os.Create(s.log)
	must(t, err)
	// LISTEN_PID must be the service's own pid, known only once it runs: the
	// shell sets it and execs.
	s.cmd = exec.Command("/bin/sh", "-c", `LISTEN_PID=$$ LISTEN_FDS=1 exec "$0" service run`, filepath.Join(dir, "xray-runner"))
	s.cmd.ExtraFiles = []*os.File{f}
	// The state directory systemd would give it: the geo databases it takes
	// stay in the test's.
	s.cmd.Env = append(os.Environ(), "STATE_DIRECTORY="+t.TempDir())
	s.cmd.Stdout, s.cmd.Stderr = out, out
	must(t, s.cmd.Start())
	_ = f.Close()
	go func() {
		_ = s.cmd.Wait()
		_ = out.Close()
		close(s.done)
	}()
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			b, _ := os.ReadFile(s.log)
			t.Logf("--- service log ---\n%s", b)
		}
	})
	return s
}

func (s *liveService) stop() {
	select {
	case <-s.done:
		return
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

func (s *liveService) connect(t *testing.T) *ipc.Client {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.Dial("unix", s.sock)
		if err == nil {
			c := ipc.NewClient(conn)
			var h ipc.HelloReply
			if err := c.Call(ctxT(t), ipc.TypeHello, ipc.Hello{Version: ipc.Version}, &h); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(h.CoreVersion, "Xray ") {
				t.Fatalf("core version %q", h.CoreVersion)
			}
			t.Cleanup(func() { _ = c.Close() })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("service does not answer: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (n *liveNet) request(killSwitch bool) ipc.StartTun {
	var req ipc.StartTun
	req.Parts.Outbounds = json.RawMessage(fmt.Sprintf(`[
		{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":%q,"port":%d,"users":[{"id":%q,"encryption":"none"}]}]}},
		{"tag":"direct","protocol":"freedom"}]`, liveServer, n.serverPort, liveUUID))
	req.LogLevel = "info"
	req.KillSwitch = killSwitch
	req.CheckURLs = []string{"http://" + liveSite + "/generate_204"}
	req.Title = "live"
	return req
}

func startTun(t *testing.T, c *ipc.Client, req ipc.StartTun) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var rep ipc.StartReply
	if err := c.Call(ctx, ipc.TypeStartTun, req, &rep); err != nil {
		t.Fatalf("start_tun: %v", err)
	}
}

func tunRouted(t *testing.T) bool {
	t.Helper()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	must(t, err)
	for _, r := range routes {
		if r.Dst != nil && r.Dst.String() == "0.0.0.0/1" {
			return true
		}
	}
	return false
}

func ourTable(t *testing.T) bool {
	t.Helper()
	conn, err := nftables.New()
	must(t, err)
	tables, err := conn.ListTables()
	must(t, err)
	for _, tb := range tables {
		if tb.Name == "xray-runner" {
			return true
		}
	}
	return false
}

// waitClean waits until the service's session has left nothing behind.
func waitClean(t *testing.T, timeout time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for tunRouted(t) || ourTable(t) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: routes %v, nft table %v", why, tunRouted(t), ourTable(t))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// fetch gets the site the ordinary way — through whatever the routing table
// says — and reports whether the server's access log saw it.
func (n *liveNet) fetch(t *testing.T) bool {
	t.Helper()
	before, _ := os.ReadFile(n.access)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + liveSite + "/x")
	if err != nil {
		t.Fatalf("GET through the tunnel: %v", err)
	}
	_ = resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		after, _ := os.ReadFile(n.access)
		if strings.Contains(string(after[len(before):]), liveSite+":80") {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// dialDirect connects to the site by the uplink, past the tunnel.
func dialDirect() error {
	d := net.Dialer{Timeout: 2 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) { serr = unix.BindToDevice(int(fd), "live-wan") })
		if err != nil {
			return err
		}
		return serr
	}}
	c, err := d.Dial("tcp4", liveSite+":80")
	if err != nil {
		return err
	}
	return c.Close()
}

func TestLiveService(t *testing.T) {
	dir := requireLive(t)
	n := liveHost(t, dir)
	sock := filepath.Join(t.TempDir(), "control.sock")
	s := startService(t, dir, sock)
	c := s.connect(t)

	t.Run("tun with kill switch", func(t *testing.T) {
		startTun(t, c, n.request(true))
		if !tunRouted(t) || !ourTable(t) {
			t.Fatalf("routes %v, kill switch table %v", tunRouted(t), ourTable(t))
		}
		if !n.fetch(t) {
			t.Error("the request did not go through the server")
		}
		if err := dialDirect(); err == nil {
			t.Error("a connection past the tunnel went through with the kill switch on")
		}
		// The health check passes through the tunnel and reaches the client.
		deadline := time.After(60 * time.Second)
		for ok := false; !ok; {
			select {
			case ev := <-c.Events():
				ok = ev.Kind == ipc.EventStatus && ev.OK
			case <-deadline:
				t.Fatal("no passing health check from the service")
			}
		}
		if err := c.Call(ctxT(t), ipc.TypeStop, struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
		waitClean(t, time.Second, "after stop")
		if err := dialDirect(); err != nil {
			t.Errorf("a connection past the tunnel fails after the session: %v", err)
		}
	})

	t.Run("dropped connection", func(t *testing.T) {
		c2 := s.connect(t)
		startTun(t, c2, n.request(false))
		_ = c2.Close()
		time.Sleep(3 * time.Second)
		if !tunRouted(t) {
			t.Fatal("the session went before its grace period was out")
		}
		waitClean(t, 30*time.Second, "after the grace period")
		// The routes go before the session itself ends.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var st ipc.StatusReply
			must(t, c.Call(ctxT(t), ipc.TypeStatus, struct{}{}, &st))
			if !st.Active {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("session still there: %+v", st)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("split", func(t *testing.T) {
		var fs unix.Statfs_t
		if err := unix.Statfs("/sys/fs/cgroup", &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
			t.Skip("the split needs cgroup v2 at /sys/fs/cgroup")
		}
		// The redirect lands in the interface's core, which is ours here.
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", xraycfg.RedirectPort))
		must(t, err)
		defer func() { _ = l.Close() }()
		sleeper := exec.Command("sleep", "60")
		must(t, sleeper.Start())
		defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()

		var rep ipc.SplitReply
		if err := c.Call(ctxT(t), ipc.TypeEnableSplit, ipc.SplitRequest{Names: []string{"sleep"}}, &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.Matched) != 1 {
			t.Fatalf("matched %v", rep.Matched)
		}
		cg, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", sleeper.Process.Pid))
		if !strings.Contains(string(cg), "xray-split") {
			t.Errorf("sleep not moved: %s", cg)
		}
		if !ourTable(t) {
			t.Error("no split rules")
		}
		if err := c.Call(ctxT(t), ipc.TypeDisableSplit, struct{}{}, nil); err != nil {
			t.Fatal(err)
		}
		waitClean(t, time.Second, "after the split")
		cg, _ = os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", sleeper.Process.Pid))
		if strings.Contains(string(cg), "xray-split") {
			t.Errorf("sleep not moved back: %s", cg)
		}
	})

	t.Run("killed service", func(t *testing.T) {
		startTun(t, c, n.request(true))
		_ = s.cmd.Process.Kill()
		<-s.done
		if !tunRouted(t) && !ourTable(t) {
			t.Log("nothing left behind by the killed service; expected the exceptions and the kill switch")
		}
		// Its next start takes down what it left (H06).
		s2 := startService(t, dir, sock)
		s2.connect(t)
		waitClean(t, 10*time.Second, "after the restart")
		routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
		must(t, err)
		for _, r := range routes {
			if r.Dst != nil && r.Dst.IP.Equal(net.ParseIP(liveServer)) && r.Dst.String() == liveServer+"/32" {
				t.Errorf("server exception left: %v", r)
			}
		}
		if err := dialDirect(); err != nil {
			t.Errorf("the network is still cut after the restart: %v", err)
		}
	})
}
