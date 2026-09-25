//go:build windows && e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// withService is the variable that lets the app use the service.
const withService = "XRAY_RUNNER_NO_SERVICE=0"

// testService installs the service from the app's folder and runs TUN
// through it (H10): the core that carries the tunnel is the service's own
// copy under Program Files, the kill switch is set up and taken down by the
// service, an app killed outright has its session taken down by the service
// once the grace period is out, and uninstall leaves nothing behind.
func (e *env) testService(t *testing.T) {
	installDir := filepath.Join(os.Getenv("ProgramFiles"), "xray-runner")
	serviceLog := filepath.Join(os.Getenv("ProgramData"), "xray-runner", "service.log")
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- service log ---\n%s", tail(serviceLog, 150))
		}
		// Whatever happened above, the machine is left without the service.
		_, _ = e.serviceCmd(t, "uninstall")
	})

	out, err := e.serviceCmd(t, "install")
	if err != nil || !strings.Contains(out, "Служба отвечает") {
		t.Fatalf("service install: %v\n%s", err, out)
	}
	if state := serviceState(t); state != svc.Running {
		t.Fatalf("service state %d after install, want running", state)
	}
	if _, err := os.Stat(filepath.Join(installDir, "bin", "xray.exe")); err != nil {
		t.Fatalf("the core was not installed: %v", err)
	}

	t.Run("tun", func(t *testing.T) {
		r := e.startApp(t, "svc-tun", "tun", withService)
		r.waitLog(t, "работаем через службу", 30*time.Second)
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
		if path := processPath(t, core); !strings.EqualFold(path, filepath.Join(installDir, "bin", "xray.exe")) {
			t.Errorf("the tunnel's core is %q, want the service's own copy", path)
		}
		if code := r.stop(t); code != 0 {
			t.Errorf("exit code %d, want 0", code)
		}
		waitExit(t, core, 30*time.Second, "the service's core outlived the session")
		for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			if got := routeAliases(t, prefix); got != "" {
				t.Errorf("route %s still there after the session, on %q", prefix, got)
			}
		}
		if status, _ := e.serviceCmd(t, "status"); !strings.Contains(status, "Сессии нет") {
			t.Errorf("service status after the session:\n%s", status)
		}
	})

	// The stand-in server's core runs from the app's folder, which the kill
	// switch does not let out by the physical adapter: with the switch on the
	// tunnel carries nothing past the server, so only the filters are checked.
	t.Run("kill switch", func(t *testing.T) {
		r := e.startApp(t, "svc-kill-switch", "tun", withService, "KILL_SWITCH=true")
		r.waitLog(t, "msg=connected", 120*time.Second)
		r.waitLog(t, "kill switch enabled", 10*time.Second)
		if !wfpFiltersPresent(t) {
			t.Errorf("no kill switch filter in WFP during the session")
		}
		if err := dialPhysical(e.alias); err == nil {
			t.Errorf("a connection by the physical adapter went through with the kill switch on")
		}
		if code := r.stop(t); code != 0 {
			t.Errorf("exit code %d, want 0", code)
		}
		if wfpFiltersPresent(t) {
			t.Errorf("kill switch filters outlived the session")
		}
		if err := dialPhysical(e.alias); err != nil {
			t.Errorf("a connection past the tunnel still fails after the session: %v", err)
		}
	})

	t.Run("killed app", func(t *testing.T) {
		r := e.startApp(t, "svc-killed", "tun", withService)
		r.waitLog(t, "msg=connected", 120*time.Second)
		core := r.corePID(t)
		r.kill(t)
		// Ten seconds of grace, then the teardown.
		waitExit(t, core, 60*time.Second, "the service kept the session of an app killed outright")
		deadline := time.Now().Add(30 * time.Second)
		for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1", serverAddr + "/32"} {
			for routeAliases(t, prefix) != "" && time.Now().Before(deadline) {
				time.Sleep(time.Second)
			}
			if got := routeAliases(t, prefix); got != "" {
				t.Errorf("route %s outlived the killed app's session, on %q", prefix, got)
			}
		}
		if journalExists(t) {
			t.Errorf("the service's teardown left the journal key behind")
		}
	})

	t.Run("uninstall", func(t *testing.T) {
		out, err := e.serviceCmd(t, "uninstall")
		if err != nil {
			t.Fatalf("service uninstall: %v\n%s", err, out)
		}
		if serviceInstalled(t) {
			t.Errorf("the service is still registered")
		}
		if _, err := os.Stat(installDir); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", installDir, err)
		}
		// Without the service the app is on its own again, and still works.
		r := e.startApp(t, "after-uninstall", "proxy", withService)
		r.waitLog(t, "msg=connected", 90*time.Second)
		if code := r.stop(t); code != 0 {
			t.Errorf("exit code %d, want 0", code)
		}
	})
}

// serviceCmd runs `xray-runner.exe service <cmd>` from the app's folder.
func (e *env) serviceCmd(t *testing.T, cmd string) (string, error) {
	t.Helper()
	c := exec.Command(filepath.Join(e.appDir, "xray-runner.exe"), "service", cmd)
	c.Dir = e.appDir
	out, err := c.CombinedOutput()
	t.Logf("service %s:\n%s", cmd, out)
	return string(out), err
}

func serviceInstalled(t *testing.T) bool {
	t.Helper()
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService("xray-runner")
	if err != nil {
		return false
	}
	_ = s.Close()
	return true
}

func serviceState(t *testing.T) svc.State {
	t.Helper()
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService("xray-runner")
	if err != nil {
		t.Fatalf("open the service: %v", err)
	}
	defer func() { _ = s.Close() }()
	st, err := s.Query()
	if err != nil {
		t.Fatal(err)
	}
	return st.State
}

// processPath is the executable of a running process.
func processPath(t *testing.T, pid int) string {
	t.Helper()
	// CIM, not Get-Process: the latter reads the path out of the process,
	// which a SYSTEM process may not allow.
	return strings.TrimSpace(ps(t, `(Get-CimInstance Win32_Process -Filter "ProcessId=`+strconv.Itoa(pid)+`").ExecutablePath`))
}
