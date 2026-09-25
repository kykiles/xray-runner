//go:build windows && e2e

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// serverAddr is where the stand-in VPN server listens. Not 127.0.0.1 and
	// not an address of the adapter: Windows keeps a /32 route for each of
	// those, and the tun preflight rightly refuses to route while a route with
	// the exception's prefix is already there. 127.0.0.2 is covered by the
	// loopback /8 alone.
	serverAddr = "127.0.0.2"
	serverUUID = "5b0f3c2e-8d4a-4f1e-9c3b-7a6d2e1f0a9b"
	// target is reached by IP, so no DNS answer decides which path it takes.
	target = "1.1.1.1"
	// targetLog is how the server's access log names a connection to target.
	targetLog = "tcp:" + target + ":80"
	// ourProxy is what the app writes into the system proxy settings.
	ourProxy = "127.0.0.1:10809"
)

// TestWindowsEndToEnd runs the built app against the real core, the real
// routing table and the real system proxy, with a local VLESS server standing
// in for the VPN. The subtests run in order and each puts the machine back.
func TestWindowsEndToEnd(t *testing.T) {
	coreDir := os.Getenv("E2E_CORE_DIR")
	if coreDir == "" {
		t.Skip("E2E_CORE_DIR is not set: point it at an unpacked Xray-windows-64 release")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("the end-to-end run needs an elevated shell: TUN routes and the tun adapter need administrator rights")
	}
	e := newEnv(t, coreDir)

	t.Run("proxy", e.testProxy)
	t.Run("tun", e.testTun)
	t.Run("killed app", e.testKilledApp)
}

// env is what every subtest shares: the built app with the core beside it and
// the stand-in server.
type env struct {
	appDir string
	logDir string
	link   string
	server *server
}

func newEnv(t *testing.T, coreDir string) *env {
	logDir := os.Getenv("E2E_LOG_DIR")
	if logDir == "" {
		logDir = t.TempDir()
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{appDir: t.TempDir(), logDir: logDir}
	buildApp(t, coreDir, e.appDir)
	alias := physicalAlias(t)
	t.Logf("physical adapter: %s", alias)
	e.server = startServer(t, coreDir, logDir, alias)
	e.link = fmt.Sprintf("vless://%s@%s:%d?type=tcp&security=none&encryption=none#e2e", serverUUID, serverAddr, e.server.port)
	return e
}

// testProxy: the system proxy points at the app while the session is up,
// traffic sent to it reaches the VPN server, and the settings come back as they
// were once the app is stopped the way a user stops it.
func (e *env) testProxy(t *testing.T) {
	before := readProxyReg(t)
	r := e.startApp(t, "proxy", "proxy")
	r.waitLog(t, "msg=connected", 90*time.Second)

	if during := readProxyReg(t); during.Enable != 1 || during.Server != ourProxy {
		t.Errorf("system proxy during the session: %+v, want enabled on %s", during, ourProxy)
	}
	mark := e.server.mark()
	get(t, &url.URL{Scheme: "http", Host: ourProxy})
	e.server.waitAccess(t, mark, targetLog)

	if code := r.stop(t); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if after := readProxyReg(t); after != before {
		t.Errorf("system proxy after the session: %+v, want it back as %+v", after, before)
	}
}

// testTun: the split default goes to the tun adapter, traffic from any process
// reaches the VPN server without being pointed at a proxy, the health check
// passes through the tunnel, and a stop takes the routes and the core away.
func (e *env) testTun(t *testing.T) {
	r := e.startApp(t, "tun", "tun")
	r.waitLog(t, "msg=connected", 120*time.Second)

	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if got := routeAliases(t, prefix); !strings.Contains(got, "xray-tun") {
			t.Errorf("route %s during the session goes to %q, want xray-tun", prefix, got)
		}
	}
	mark := e.server.mark()
	get(t, nil)
	e.server.waitAccess(t, mark, targetLog)
	r.waitLog(t, "connectivity check ok", 60*time.Second)
	core := r.corePID(t)

	if code := r.stop(t); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if got := routeAliases(t, prefix); got != "" {
			t.Errorf("route %s still there after the session, on %q", prefix, got)
		}
	}
	waitExit(t, core, 15*time.Second, "the core outlived its session")
}

// testKilledApp: an app killed outright runs no teardown. Its core must go with
// it (G07), and the next run must clear the proxy setting it left on a dead
// port and leave the machine without it (A11).
func (e *env) testKilledApp(t *testing.T) {
	before := readProxyReg(t)
	t.Cleanup(func() { writeProxyReg(t, before) })

	r := e.startApp(t, "killed", "proxy")
	r.waitLog(t, "msg=connected", 90*time.Second)
	core := r.corePID(t)
	r.kill(t)
	waitExit(t, core, 15*time.Second, "the core outlived an app killed outright")

	if left := readProxyReg(t); left.Enable != 1 || left.Server != ourProxy {
		t.Logf("after the kill the system proxy is %+v; expected our setting left behind", left)
	}

	r = e.startApp(t, "after-kill", "proxy")
	r.waitLog(t, "msg=connected", 90*time.Second)
	if code := r.stop(t); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if final := readProxyReg(t); final.Enable != 0 || final.Server == ourProxy {
		t.Errorf("system proxy after the run that followed the kill: %+v, want it off and not on %s", final, ourProxy)
	}
}

// --- the app -----------------------------------------------------------------

// buildApp builds xray-runner.exe into appDir and puts the core bundle in bin/
// beside it, the way a release is laid out.
func buildApp(t *testing.T, coreDir, appDir string) {
	t.Helper()
	exe := filepath.Join(appDir, "xray-runner.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "xray-runner/cmd/xray-runner").CombinedOutput(); err != nil {
		t.Fatalf("build xray-runner: %v\n%s", err, out)
	}
	bin := filepath.Join(appDir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"xray.exe", "wintun.dll", "geoip.dat", "geosite.dat"} {
		copyFile(t, filepath.Join(coreDir, name), filepath.Join(bin, name))
	}
}

type appRun struct {
	name string
	cmd  *exec.Cmd
	log  string // the app's own log (LOG_FILE)
	out  string // its stdout and stderr
	done chan struct{}
}

// startApp runs one scripted session (--server 1) in mode, without a terminal,
// in a process group of its own so a Ctrl+Break can be sent to it alone. The
// data dir is fresh for each run.
func (e *env) startApp(t *testing.T, name, mode string) *appRun {
	t.Helper()
	r := &appRun{
		name: name,
		log:  filepath.Join(e.logDir, "app-"+name+".log"),
		out:  filepath.Join(e.logDir, "app-"+name+".out"),
		done: make(chan struct{}),
	}
	out, err := os.Create(r.out)
	if err != nil {
		t.Fatal(err)
	}
	r.cmd = exec.Command(filepath.Join(e.appDir, "xray-runner.exe"), "--server", "1")
	r.cmd.Dir = e.appDir
	r.cmd.Env = append(os.Environ(),
		"SUBSCRIPTION_URL="+e.link,
		"MODE="+mode,
		"APPDATA="+t.TempDir(),
		"LOCALAPPDATA="+t.TempDir(),
		"LOG_FILE="+r.log,
		"LOG_LEVEL=debug",
		"XRAY_LOG_LEVEL=info",
		"PROXY_SYSTEM=true",
		"KILL_SWITCH=false",
	)
	r.cmd.Stdout, r.cmd.Stderr = out, out
	r.cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := r.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		_ = r.cmd.Wait()
		_ = out.Close()
		close(r.done)
	}()
	t.Cleanup(func() {
		if !r.exited() {
			_ = r.interrupt()
			select {
			case <-r.done:
			case <-time.After(30 * time.Second):
				_ = r.cmd.Process.Kill()
				<-r.done
			}
		}
		if t.Failed() {
			t.Logf("--- %s: app log ---\n%s", name, tail(r.log, 120))
			t.Logf("--- %s: app output ---\n%s", name, tail(r.out, 40))
		}
	})
	return r
}

func (r *appRun) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// waitLog waits for substr in the app's log. An app that exits first fails
// the test with its exit code.
func (r *appRun) waitLog(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(r.log); err == nil && strings.Contains(string(data), substr) {
			return
		}
		if r.exited() {
			t.Fatalf("%s exited with code %d before logging %q", r.name, r.cmd.ProcessState.ExitCode(), substr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not log %q within %s", r.name, substr, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

var corePIDLine = regexp.MustCompile(`msg="xray started" pid=(\d+)`)

// corePID is the pid of the core the app started last.
func (r *appRun) corePID(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(r.log)
	if err != nil {
		t.Fatal(err)
	}
	m := corePIDLine.FindAllStringSubmatch(string(data), -1)
	if len(m) == 0 {
		t.Fatalf("%s: no core pid in the log", r.name)
	}
	pid, _ := strconv.Atoi(m[len(m)-1][1])
	return pid
}

// interrupt sends Ctrl+Break to the app's process group — what the app gets
// from Ctrl+C on its own console, and handles the same way.
func (r *appRun) interrupt() error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(r.cmd.Process.Pid))
}

// stop interrupts the app and returns its exit code once it has cleaned up.
func (r *appRun) stop(t *testing.T) int {
	t.Helper()
	if err := r.interrupt(); err != nil {
		t.Fatalf("send Ctrl+Break to %s: %v", r.name, err)
	}
	select {
	case <-r.done:
		return r.cmd.ProcessState.ExitCode()
	case <-time.After(60 * time.Second):
		t.Fatalf("%s did not exit within 60s of Ctrl+Break", r.name)
		return -1
	}
}

// kill ends the app outright, the way Task Manager does: no handler, no
// teardown.
func (r *appRun) kill(t *testing.T) {
	t.Helper()
	if err := r.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill %s: %v", r.name, err)
	}
	<-r.done
}

// --- the stand-in VPN server -------------------------------------------------

type server struct {
	port   int
	access string
}

// startServer runs the core as a plain VLESS server on serverAddr. Its freedom
// outbound is pinned to the physical adapter: with the tunnel up, the routing
// table would send the server's own traffic back into it. It resolves names
// itself for the same reason.
func startServer(t *testing.T, coreDir, logDir, alias string) *server {
	t.Helper()
	s := &server{port: freePort(t, serverAddr), access: filepath.Join(logDir, "server-access.log")}
	cfg := map[string]any{
		"log": map[string]any{
			"loglevel": "info",
			"access":   s.access,
			"error":    filepath.Join(logDir, "server-error.log"),
		},
		"dns": map[string]any{"servers": []string{"8.8.8.8", "1.1.1.1"}},
		"inbounds": []any{map[string]any{
			"listen":   serverAddr,
			"port":     s.port,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": serverUUID}},
				"decryption": "none",
			},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"outbounds": []any{map[string]any{
			"protocol":       "freedom",
			"settings":       map[string]any{"domainStrategy": "UseIPv4"},
			"streamSettings": map[string]any{"sockopt": map[string]any{"interface": alias}},
		}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(logDir, "server.out"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(coreDir, "xray.exe"), "run", "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = out.Close()
	})

	addr := net.JoinHostPort(serverAddr, strconv.Itoa(s.port))
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			_ = c.Close()
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not listen on %s\n%s", addr, tail(filepath.Join(logDir, "server.out"), 40))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// mark is how much of the access log has been written so far; waitAccess
// looks only past it.
func (s *server) mark() int {
	data, _ := os.ReadFile(s.access)
	return len(data)
}

// waitAccess waits for the server to log a connection matching substr after
// mark — the proof that the traffic went through it.
func (s *server) waitAccess(t *testing.T, mark int, substr string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(s.access)
		if len(data) > mark && strings.Contains(string(data[mark:]), substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the VPN server logged no %q — the traffic did not go through it\n%s", substr, tail(s.access, 20))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- the machine -------------------------------------------------------------

// get fetches http://target/ through proxy, or straight when proxy is nil, and
// fails unless an answer comes back. A few tries: a fresh tunnel may need a
// moment before the first connection gets through.
func get(t *testing.T, proxy *url.URL) {
	t.Helper()
	tr := &http.Transport{Proxy: nil}
	if proxy != nil {
		tr.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{
		Timeout:       15 * time.Second,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	var lastErr error
	for range 5 {
		resp, err := client.Get("http://" + target + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("GET http://%s/ (proxy %v): %v", target, proxy, lastErr)
}

// ps runs a PowerShell script and returns its trimmed output.
func ps(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[Text.Encoding]::UTF8; "+script).CombinedOutput()
	if err != nil {
		t.Fatalf("powershell %q: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// physicalAlias names the adapter that carries the machine's internet traffic,
// found the way the app finds it.
func physicalAlias(t *testing.T) string {
	t.Helper()
	return ps(t, `$r = Find-NetRoute -RemoteIPAddress '1.1.1.1' -ErrorAction Stop | Select-Object -First 1; (Get-NetAdapter -InterfaceIndex $r.InterfaceIndex -ErrorAction Stop).InterfaceAlias`)
}

// routeAliases lists the adapters holding a route with exactly this prefix,
// empty when there is none. Get-NetRoute finding nothing fails the script even
// when told to continue silently, hence the explicit exit.
func routeAliases(t *testing.T, prefix string) string {
	t.Helper()
	return ps(t, fmt.Sprintf(`Get-NetRoute -DestinationPrefix '%s' -PolicyStore ActiveStore -ErrorAction SilentlyContinue | ForEach-Object { $_.InterfaceAlias }; exit 0`, prefix))
}

// waitExit fails unless the process is gone within timeout.
func waitExit(t *testing.T, pid int, timeout time.Duration, why string) {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Errorf("%s: pid %d still running after %s", why, pid, timeout)
	}
}

func freePort(t *testing.T, host string) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("listen on %s: %v", host, err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("core bundle: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// tail is the last n lines of a file, for a failure message.
func tail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// --- the system proxy settings -----------------------------------------------

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// proxyReg is the three values the app touches; a value that is absent reads
// as its zero value with the Set flag false.
type proxyReg struct {
	Enable                            uint64
	Server, Override                  string
	EnableSet, ServerSet, OverrideSet bool
}

func readProxyReg(t *testing.T) proxyReg {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open %s: %v", internetSettings, err)
	}
	defer func() { _ = k.Close() }()
	var p proxyReg
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		p.Enable, p.EnableSet = v, true
	} else if !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("read ProxyEnable: %v", err)
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		p.Server, p.ServerSet = v, true
	} else if !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("read ProxyServer: %v", err)
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		p.Override, p.OverrideSet = v, true
	} else if !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("read ProxyOverride: %v", err)
	}
	return p
}

// writeProxyReg puts the three values back as p has them, for the cleanup
// after a kill that left the app's setting behind.
func writeProxyReg(t *testing.T, p proxyReg) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.SET_VALUE)
	if err != nil {
		t.Errorf("open %s: %v", internetSettings, err)
		return
	}
	defer func() { _ = k.Close() }()
	set := func(name string, exists bool, write func() error) {
		var err error
		if exists {
			err = write()
		} else if err = k.DeleteValue(name); errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		if err != nil {
			t.Errorf("restore %s: %v", name, err)
		}
	}
	set("ProxyEnable", p.EnableSet, func() error { return k.SetDWordValue("ProxyEnable", uint32(p.Enable)) })
	set("ProxyServer", p.ServerSet, func() error { return k.SetStringValue("ProxyServer", p.Server) })
	set("ProxyOverride", p.OverrideSet, func() error { return k.SetStringValue("ProxyOverride", p.Override) })
}
