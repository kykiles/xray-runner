//go:build linux

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

// TunSwitchesDNS reports whether a tun session moves the system's DNS into the
// tunnel (EnableTunDNS), and so whether the core must reach its servers
// without the system resolver (xraycfg.PinServerAddresses).
const TunSwitchesDNS = true

// tunDNSServers are the resolvers the tun link is given. They are reached
// through the tunnel like any other address; 1.1.1.1 is the one the split's
// DNS redirect already uses.
var tunDNSServers = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}

// resolvConf is where the C library learns its resolver; overridable in tests.
var resolvConf = "/etc/resolv.conf"

// resolvedCaller calls a method of systemd-resolved's manager. The real one
// speaks D-Bus; tests record the calls.
type resolvedCaller interface {
	call(method string, args ...any) error
}

// resolved is overridable in tests.
var resolved resolvedCaller = busResolved{}

// linkIndex finds an interface's index; overridable in tests.
var linkIndex = func(name string) (int, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return 0, err
	}
	return ifi.Index, nil
}

// EnableTunDNS sends the system's DNS into the tunnel. Left alone, systemd-
// resolved keeps asking the resolver DHCP gave the physical link — the router,
// an address on the LAN, which the tunnel's routes never cover — and every
// name the machine looks up goes past the VPN to the provider.
//
// The tun link gets resolvers of its own, the route-only domain "~." and the
// default route for DNS: by longest-suffix match "~." takes every name no link
// claims more specifically, so the physical link's resolver is left out of the
// lookups without its settings being touched — they belong to NetworkManager
// or networkd, and reverting them would take them away for good.
//
// Nothing needs undoing after a run that died: resolved keeps these settings
// on the link, and the link goes when the core does.
//
// An error means the DNS still goes the old way; it is reported, not fatal.
func EnableTunDNS(iface, _ string) error {
	idx, err := linkIndex(iface)
	if err != nil {
		return fmt.Errorf("интерфейс %s: %w", iface, err)
	}
	type addr struct {
		Family  int32
		Address []byte
	}
	var servers []addr
	for _, s := range tunDNSServers {
		b := s.As4()
		servers = append(servers, addr{Family: 2 /* AF_INET */, Address: b[:]})
	}
	type domain struct {
		Domain    string
		RouteOnly bool
	}
	steps := []struct {
		method string
		arg    any
	}{
		{"SetLinkDNS", servers},
		{"SetLinkDomains", []domain{{".", true}}},
		{"SetLinkDefaultRoute", true},
	}
	for _, s := range steps {
		if err := resolved.call(s.method, int32(idx), s.arg); err != nil {
			// Half a setting is worse than none: servers without "~." share the
			// lookups with the router and pass for a fix.
			_ = resolved.call("RevertLink", int32(idx))
			return resolvedError(err)
		}
	}
	if stub, other := resolvConfStub(); !stub {
		return fmt.Errorf("в %s указан не systemd-resolved (nameserver %s) — программы спрашивают его напрямую", resolvConf, other)
	}
	slog.Info("tun dns enabled", "iface", iface, "servers", tunDNSServers)
	return nil
}

// DisableTunDNS hands the tun link's DNS back to resolved's defaults. A link
// that is already gone took its settings with it, and that is success.
func DisableTunDNS(iface string) error {
	idx, err := linkIndex(iface)
	if err != nil {
		return nil
	}
	if err := resolved.call("RevertLink", int32(idx)); err != nil {
		if strings.HasSuffix(busErrorName(err), ".NoSuchLink") {
			return nil
		}
		return resolvedError(err)
	}
	return nil
}

// resolvedError says in plain words why resolved refused.
func resolvedError(err error) error {
	switch busErrorName(err) {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
		return errors.New("systemd-resolved не запущен")
	case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired":
		return errors.New("systemd-resolved не дал менять DNS: нужен root или служба")
	}
	return fmt.Errorf("systemd-resolved: %w", err)
}

// busErrorName is the D-Bus error name behind err, "" when it is not one. The
// bus hands remote errors back as values, the library's own as pointers.
func busErrorName(err error) string {
	if de, ok := errors.AsType[dbus.Error](err); ok {
		return de.Name
	}
	if de, ok := errors.AsType[*dbus.Error](err); ok && de != nil {
		return de.Name
	}
	return ""
}

// resolvConfStub reports whether every nameserver in resolv.conf is resolved's
// stub; when not, the first one that is not. A file that cannot be read says
// nothing either way and counts as the stub.
func resolvConfStub() (bool, string) {
	data, err := os.ReadFile(resolvConf)
	if err != nil {
		return true, ""
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		if f[1] != "127.0.0.53" && f[1] != "127.0.0.54" {
			return false, f[1]
		}
	}
	return true, ""
}

// busResolved calls resolved over the system bus, a connection per call: a
// session sets the link up once per core, and a long-lived connection would be
// one more thing to tear down.
type busResolved struct{}

func (busResolved) call(method string, args ...any) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("системная шина D-Bus: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").
		Call("org.freedesktop.resolve1.Manager."+method, 0, args...).Err
}
