//go:build windows

package system

import (
	"errors"
	"strings"
	"testing"
)

// fakeIP models powershell + route.exe: it answers the two lookup commands
// with canned output and records every mutating route command.
type fakeIP struct {
	findRoute   string
	findErr     error
	tunIndex    string
	tunIndexErr error
	cmds        []string
}

func (f *fakeIP) lookPath(bin string) error { return nil }

func (f *fakeIP) run(bin string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "Find-NetRoute") {
		return []byte(f.findRoute), f.findErr
	}
	if strings.Contains(joined, "Get-NetAdapter") {
		return []byte(f.tunIndex), f.tunIndexErr
	}
	f.cmds = append(f.cmds, bin+" "+joined)
	return nil, nil
}

func (f *fakeIP) has(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	orig := ipCmd
	ipCmd = f
	t.Cleanup(func() { ipCmd = orig })
}

func newFakeIP() *fakeIP {
	return &fakeIP{findRoute: "192.168.31.1 12\r\n", tunIndex: "27\r\n"}
}

var tunCfg = TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"}}

// Without these two routes the wintun adapter exists but no traffic ever
// enters it — the bug this whole file addresses.
func TestEnableTunRouting_SendsDefaultTrafficIntoTun(t *testing.T) {
	f := newFakeIP()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 0.0.0.0 mask 128.0.0.0 10.0.0.1 if 27") {
		t.Errorf("missing lower split-default route, got: %v", f.cmds)
	}
	if !f.has("route add 128.0.0.0 mask 128.0.0.0 10.0.0.1 if 27") {
		t.Errorf("missing upper split-default route, got: %v", f.cmds)
	}
}

// The tunnel's own packets to the VPS must keep using the physical adapter,
// otherwise xray's uplink routes into the tun it is serving and deadlocks.
func TestEnableTunRouting_ExcludesServerViaPhysicalGateway(t *testing.T) {
	f := newFakeIP()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 45.150.32.235 mask 255.255.255.255 192.168.31.1 if 12") {
		t.Errorf("missing server exclusion route, got: %v", f.cmds)
	}
}

// Installing the split default before knowing the server's physical path would
// blackhole the tunnel's own uplink, so this must fail before touching routes.
func TestEnableTunRouting_FailsWithoutTouchingRoutesWhenServerPathUnknown(t *testing.T) {
	f := newFakeIP()
	f.findErr = errors.New("no route")
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the server's physical route can't be resolved")
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

func TestEnableTunRouting_FailsWhenTunAdapterMissing(t *testing.T) {
	f := newFakeIP()
	f.tunIndexErr = errors.New("adapter not found")
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the tun adapter has no interface index")
	}
}

func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	f := newFakeIP()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	DisableTunRouting()

	for _, want := range []string{
		"route delete 0.0.0.0 mask 128.0.0.0",
		"route delete 128.0.0.0 mask 128.0.0.0",
		"route delete 45.150.32.235 mask 255.255.255.255",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not fire stray `route delete` at the host.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := newFakeIP()
	withFakeIP(t, f)

	DisableTunRouting()

	if len(f.cmds) != 0 {
		t.Errorf("expected no commands, got: %v", f.cmds)
	}
}

func TestParseFindNetRoute(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		hop     string
		idx     string
		wantErr bool
	}{
		{name: "via gateway", out: "192.168.31.1 12\r\n", hop: "192.168.31.1", idx: "12"},
		{name: "on-link", out: "0.0.0.0 12\r\n", hop: "0.0.0.0", idx: "12"},
		{name: "missing index", out: "192.168.31.1\r\n", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hop, idx, err := parseFindNetRoute(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFindNetRoute: %v", err)
			}
			if hop != tt.hop || idx != tt.idx {
				t.Errorf("got hop=%q idx=%q, want hop=%q idx=%q", hop, idx, tt.hop, tt.idx)
			}
		})
	}
}
