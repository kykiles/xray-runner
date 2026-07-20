//go:build linux

package system

import (
	"fmt"
	"strings"
	"testing"
)

// fakeDesktop models gsettings/kwriteconfig5: canned answers for reads, a log
// of every write. Absent binaries report an error, the way exec does when the
// desktop environment isn't installed.
type fakeDesktop struct {
	replies map[string]string // "bin key" -> stdout
	missing map[string]bool   // binaries that aren't installed
	writes  []string
}

func newFakeDesktop() *fakeDesktop {
	return &fakeDesktop{replies: map[string]string{}, missing: map[string]bool{}}
}

func (f *fakeDesktop) call(bin string, args ...string) ([]byte, error) {
	if f.missing[bin] {
		return nil, fmt.Errorf("exec: %q: executable file not found in $PATH", bin)
	}
	joined := bin + " " + strings.Join(args, " ")
	if len(args) > 0 && (args[0] == "set" || bin == "kwriteconfig5") {
		f.writes = append(f.writes, joined)
		return nil, nil
	}
	for k, v := range f.replies {
		if strings.HasPrefix(joined, k) {
			return []byte(v), nil
		}
	}
	return []byte(""), nil
}

func (f *fakeDesktop) wrote(substr string) bool {
	for _, w := range f.writes {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func withFakeDesktop(t *testing.T, f *fakeDesktop) {
	t.Helper()
	orig := desktopCmd
	desktopCmd = f.call
	t.Cleanup(func() { desktopCmd = orig })
}

// A desktop configured for PAC has mode=auto, which is not "manual" and so
// reads back as disabled. Restoring it must not collapse into "none" — that
// silently drops the user's proxy configuration.
func TestWriteProxyState_RestoresAutoMode(t *testing.T) {
	f := newFakeDesktop()
	f.replies["gsettings get org.gnome.system.proxy mode"] = "'auto'\n"
	withFakeDesktop(t, f)

	saved := ReadProxyState()
	if saved.Enabled {
		t.Fatalf("auto mode is not a manual proxy, got Enabled=true")
	}
	if saved.Mode != "auto" {
		t.Fatalf("Mode = %q, want auto", saved.Mode)
	}

	f.writes = nil
	if err := WriteProxyState(saved); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if !f.wrote("proxy mode auto") {
		t.Errorf("auto mode was not restored, wrote: %v", f.writes)
	}
	if f.wrote("proxy mode none") {
		t.Errorf("restoring auto must not fall back to none, wrote: %v", f.writes)
	}
}

// A desktop that genuinely had no proxy still has to end up at none.
func TestWriteProxyState_RestoresNoneMode(t *testing.T) {
	f := newFakeDesktop()
	f.replies["gsettings get org.gnome.system.proxy mode"] = "'none'\n"
	withFakeDesktop(t, f)

	saved := ReadProxyState()
	f.writes = nil
	if err := WriteProxyState(saved); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if !f.wrote("proxy mode none") {
		t.Errorf("none mode was not restored, wrote: %v", f.writes)
	}
}

// KDE's ProxyType 2 is PAC. Same rule as GNOME's auto.
func TestWriteProxyState_RestoresKDEProxyType(t *testing.T) {
	f := newFakeDesktop()
	f.missing["gsettings"] = true
	f.replies["kreadconfig5 --group Proxy --key ProxyType"] = "2\n"
	withFakeDesktop(t, f)

	saved := ReadProxyState()
	if saved.Mode != "2" {
		t.Fatalf("Mode = %q, want 2", saved.Mode)
	}

	f.writes = nil
	if err := WriteProxyState(saved); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if !f.wrote("--key ProxyType 2") {
		t.Errorf("KDE ProxyType was not restored, wrote: %v", f.writes)
	}
}

// A GNOME state reaching the KDE writer (gsettings broke between read and
// write) carries "auto", which KDE cannot parse. It must fall back to 0 rather
// than write a garbage ProxyType.
func TestWriteProxyState_RejectsForeignModeForKDE(t *testing.T) {
	f := newFakeDesktop()
	f.missing["gsettings"] = true
	withFakeDesktop(t, f)

	if err := WriteProxyState(ProxyState{Mode: "auto"}); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if f.wrote("--key ProxyType auto") {
		t.Errorf("a GNOME mode must not be written to KDE, wrote: %v", f.writes)
	}
	if !f.wrote("--key ProxyType 0") {
		t.Errorf("expected fallback to ProxyType 0, wrote: %v", f.writes)
	}
}

// The port key is an int. A saved server with no port used to send an empty
// string, failing the write and rolling the whole restore back to none.
func TestWriteProxyState_SurvivesServerWithoutPort(t *testing.T) {
	f := newFakeDesktop()
	withFakeDesktop(t, f)

	if err := WriteProxyState(ProxyState{Enabled: true, Server: "proxy.corp"}); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if !f.wrote("proxy.http host proxy.corp") {
		t.Errorf("host was not restored, wrote: %v", f.writes)
	}
	if !f.wrote("proxy.http port 0") {
		t.Errorf("empty port must become 0, wrote: %v", f.writes)
	}
	if f.wrote("proxy mode none") {
		t.Errorf("restore must not roll back, wrote: %v", f.writes)
	}
}

// KDE stores host and port in one string, where a trailing colon is not a
// valid address.
func TestWriteKDEManual_OmitsTrailingColon(t *testing.T) {
	f := newFakeDesktop()
	f.missing["gsettings"] = true
	withFakeDesktop(t, f)

	if err := WriteProxyState(ProxyState{Enabled: true, Server: "proxy.corp"}); err != nil {
		t.Fatalf("WriteProxyState: %v", err)
	}
	if f.wrote("httpProxy proxy.corp:") {
		t.Errorf("portless host must stay bare, wrote: %v", f.writes)
	}
	if !f.wrote("httpProxy proxy.corp") {
		t.Errorf("host was not written, wrote: %v", f.writes)
	}
}

func TestReadProxyState_ReadsManualProxy(t *testing.T) {
	f := newFakeDesktop()
	f.replies["gsettings get org.gnome.system.proxy mode"] = "'manual'\n"
	f.replies["gsettings get org.gnome.system.proxy.http host"] = "'127.0.0.1'\n"
	f.replies["gsettings get org.gnome.system.proxy.http port"] = "10809\n"
	withFakeDesktop(t, f)

	s := ReadProxyState()
	if !s.Enabled {
		t.Errorf("Enabled = false, want true for manual mode")
	}
	if s.Server != "127.0.0.1:10809" {
		t.Errorf("Server = %q, want 127.0.0.1:10809", s.Server)
	}
}
