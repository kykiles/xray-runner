//go:build linux

package system

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// fakeResolved records resolved's calls and fails the one named in failOn.
type fakeResolved struct {
	calls  []string
	sigs   []string
	failOn string
	err    error
}

func (f *fakeResolved) call(method string, args ...any) error {
	f.calls = append(f.calls, fmt.Sprintf("%s%v", method, args))
	f.sigs = append(f.sigs, method+" "+dbus.SignatureOf(args...).String())
	if method == f.failOn {
		return f.err
	}
	return nil
}

func withFakeResolved(t *testing.T, conf string) *fakeResolved {
	t.Helper()
	f := &fakeResolved{}
	origR, origIdx, origConf := resolved, linkIndex, resolvConf
	resolved = f
	linkIndex = func(name string) (int, error) {
		if name != "xray-tun" {
			return 0, errors.New("no such interface")
		}
		return 7, nil
	}
	resolvConf = filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(resolvConf, []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resolved, linkIndex, resolvConf = origR, origIdx, origConf })
	return f
}

const stubConf = "# managed by resolved\nnameserver 127.0.0.53\noptions edns0 trust-ad\nsearch .\n"

// The tun link gets resolvers, "~." and the DNS default route — all three, on
// its own index, and nothing on any other link.
func TestEnableTunDNS_SetsTheTunLink(t *testing.T) {
	f := withFakeResolved(t, stubConf)
	if err := EnableTunDNS("xray-tun", ""); err != nil {
		t.Fatalf("EnableTunDNS: %v", err)
	}
	want := []string{
		"SetLinkDNS[7 [{2 [1 1 1 1]} {2 [8 8 8 8]}]]",
		"SetLinkDomains[7 [{. true}]]",
		"SetLinkDefaultRoute[7 true]",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	// The signatures resolved declares (busctl introspect org.freedesktop.resolve1).
	wantSigs := "SetLinkDNS ia(iay),SetLinkDomains ia(sb),SetLinkDefaultRoute ib"
	if got := strings.Join(f.sigs, ","); got != wantSigs {
		t.Errorf("signatures = %s, want %s", got, wantSigs)
	}
}

// Servers without "~." would share lookups with the router and look like a
// fix: a step that fails takes the link's settings back.
func TestEnableTunDNS_FailedStepReverts(t *testing.T) {
	f := withFakeResolved(t, stubConf)
	f.failOn, f.err = "SetLinkDomains", dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}
	err := EnableTunDNS("xray-tun", "")
	if err == nil || !strings.Contains(err.Error(), "нужен root") {
		t.Fatalf("err = %v, want the access refusal in plain words", err)
	}
	if last := f.calls[len(f.calls)-1]; last != "RevertLink[7]" {
		t.Errorf("last call = %s, want RevertLink[7]", last)
	}
}

func TestEnableTunDNS_NoResolved(t *testing.T) {
	f := withFakeResolved(t, stubConf)
	f.failOn, f.err = "SetLinkDNS", dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}
	if err := EnableTunDNS("xray-tun", ""); err == nil || !strings.Contains(err.Error(), "не запущен") {
		t.Fatalf("err = %v, want «systemd-resolved не запущен»", err)
	}
}

// A resolv.conf that names a resolver other than the stub sends the C library
// past resolved, so the switch does not hold, and the caller hears it.
func TestEnableTunDNS_ResolvConfPastTheStub(t *testing.T) {
	withFakeResolved(t, "nameserver 127.0.0.53\nnameserver 192.168.31.1\n")
	err := EnableTunDNS("xray-tun", "")
	if err == nil || !strings.Contains(err.Error(), "192.168.31.1") {
		t.Fatalf("err = %v, want the foreign nameserver named", err)
	}
}

func TestEnableTunDNS_NoInterface(t *testing.T) {
	f := withFakeResolved(t, stubConf)
	if err := EnableTunDNS("missing", ""); err == nil {
		t.Fatal("no error for a missing interface")
	}
	if len(f.calls) != 0 {
		t.Errorf("resolved called for a missing interface: %v", f.calls)
	}
}

// Disable reverts the tun link; a link already gone took its settings with it.
func TestDisableTunDNS(t *testing.T) {
	f := withFakeResolved(t, stubConf)
	if err := DisableTunDNS("xray-tun"); err != nil {
		t.Fatalf("DisableTunDNS: %v", err)
	}
	if strings.Join(f.calls, ",") != "RevertLink[7]" {
		t.Errorf("calls = %v", f.calls)
	}

	f.calls = nil
	if err := DisableTunDNS("missing"); err != nil || len(f.calls) != 0 {
		t.Errorf("missing link: err=%v calls=%v", err, f.calls)
	}

	f.failOn, f.err = "RevertLink", dbus.Error{Name: "org.freedesktop.resolve1.NoSuchLink"}
	if err := DisableTunDNS("xray-tun"); err != nil {
		t.Errorf("link gone between lookup and revert: %v", err)
	}
}

func TestResolvConfStub(t *testing.T) {
	cases := map[string]bool{
		stubConf:                    true,
		"nameserver 127.0.0.54\n":   true,
		"":                          true,
		"nameserver 192.168.31.1\n": false,
		"# nameserver 1.1.1.1\nnameserver 127.0.0.53\n": true,
	}
	for conf, want := range cases {
		withFakeResolved(t, conf)
		if got, _ := resolvConfStub(); got != want {
			t.Errorf("%q: stub = %v, want %v", conf, got, want)
		}
	}
}
