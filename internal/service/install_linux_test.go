//go:build linux

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit gives the service the network and nothing that amounts to root:
// no CAP_SYS_ADMIN, CAP_DAC_OVERRIDE or CAP_SYS_PTRACE, and a read-only file
// system but for the socket's directory and the cgroup tree.
func TestServiceUnitRights(t *testing.T) {
	var bounding string
	for _, line := range strings.Split(serviceUnitText, "\n") {
		if v, ok := strings.CutPrefix(line, "CapabilityBoundingSet="); ok {
			bounding = v
		}
	}
	if bounding != "CAP_NET_ADMIN CAP_NET_RAW" {
		t.Fatalf("bounding set %q", bounding)
	}
	for _, want := range []string{"ProtectSystem=strict", "ProtectHome=yes", "NoNewPrivileges=yes",
		"ExecStart=" + InstallDir + "/xray-runner service run", "DeviceAllow=/dev/net/tun rw"} {
		if !strings.Contains(serviceUnitText, want+"\n") {
			t.Errorf("unit lacks %q", want)
		}
	}
	for _, want := range []string{"SocketMode=0660", "SocketGroup=xray-runner", "ListenStream=/run/xray-runner/control.sock"} {
		if !strings.Contains(socketUnitText, want+"\n") {
			t.Errorf("socket unit lacks %q", want)
		}
	}
}

// systemd reads both units without complaint.
func TestUnitsVerify(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("no systemd-analyze")
	}
	dir := t.TempDir()
	svc := strings.ReplaceAll(serviceUnitText, InstallDir+"/xray-runner", "/bin/true")
	for name, text := range map[string]string{serviceUnit: svc, socketUnit: socketUnitText} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("systemd-analyze", "verify", "--man=no", filepath.Join(dir, serviceUnit), filepath.Join(dir, socketUnit)).CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(out))) > 0 {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, out)
	}
}

// An install copies the program under its fixed name and the core with its
// files into bin/, replacing what an earlier install left.
func TestCopyInto(t *testing.T) {
	src := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(src, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := payload{
		exe:  write("xray-runner-linux-amd64", "prog", 0o755),
		core: write("xray", "core", 0o755),
		aux:  []string{write("geoip.dat", "geo", 0o644)},
	}
	dst := filepath.Join(t.TempDir(), "xray-runner")
	for range 2 {
		if err := p.copyInto(dst); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"xray-runner": "prog", "bin/xray": "core", "bin/geoip.dat": "geo"} {
		b, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
	if st, _ := os.Stat(filepath.Join(dst, "bin/xray")); st.Mode()&0o111 == 0 {
		t.Error("core not executable")
	}
	if st, _ := os.Stat(filepath.Join(dst, "bin/geoip.dat")); st.Mode()&0o022 != 0 {
		t.Errorf("geoip.dat mode %v", st.Mode())
	}
}
