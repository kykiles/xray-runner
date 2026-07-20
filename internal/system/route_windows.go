//go:build windows

package system

import (
	"fmt"
	"log/slog"
	"strings"

	"xray-runner/internal/xraycfg"
)

// splitDefault covers the whole IPv4 space in two halves. Each is more
// specific than the existing 0.0.0.0/0, so it wins by prefix length without
// deleting the physical default route — which stays available for the
// server exception below and for a clean teardown.
var splitDefault = []struct{ dest, mask string }{
	{"0.0.0.0", "128.0.0.0"},
	{"128.0.0.0", "128.0.0.0"},
}

// ipCmd is overridable in tests.
var ipCmd commander = execCommander{}

// installed remembers what EnableTunRouting added so teardown removes exactly
// that — and nothing that belongs to another VPN client on the host.
var installed *TunRouteConfig

// bindProbe is the destination used to learn which adapter carries the host's
// internet traffic. The VPN servers cannot stand in for it: a server on the
// local link says nothing about the physical default path.
const bindProbe = "1.1.1.1"

// DirectBind reports how freedom outbounds leave the tunnel on this platform.
// Windows drops sockopt.mark on the floor — xray implements it for Linux only —
// so the sockets are pinned to the physical adapter instead. Xray resolves the
// name through net.InterfaceByName and applies IP_UNICAST_IF, which picks the
// outgoing adapter directly and so ignores the split default route.
//
// This must run before EnableTunRouting: once the split default is in place,
// Find-NetRoute answers with the tun adapter.
func DirectBind() (xraycfg.DirectBind, error) {
	out, err := powershell(fmt.Sprintf(
		`$r = Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop | Select-Object -First 1; (Get-NetAdapter -InterfaceIndex $r.InterfaceIndex -ErrorAction Stop).InterfaceAlias`,
		bindProbe))
	if err != nil {
		return xraycfg.DirectBind{}, fmt.Errorf("определить физический адаптер: %w\n%s", err, out)
	}
	alias := strings.TrimSpace(string(out))
	if alias == "" {
		return xraycfg.DirectBind{}, fmt.Errorf("физический адаптер не найден")
	}
	return xraycfg.DirectBind{Interface: alias}, nil
}

// parseFindNetRoute reads the "<next hop> <interface index>" line produced by
// the Find-NetRoute call below. An on-link destination reports 0.0.0.0.
func parseFindNetRoute(out string) (nextHop, ifIndex string, err error) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", "", fmt.Errorf("unexpected Find-NetRoute output: %q", out)
	}
	return fields[0], fields[1], nil
}

func powershell(script string) ([]byte, error) {
	return ipCmd.run("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

// EnableTunRouting points the system's default traffic at the TUN adapter and
// pins the VPN server to the physical adapter.
func EnableTunRouting(cfg TunRouteConfig) error {
	if len(cfg.ServerIPs) == 0 {
		return fmt.Errorf("не задан адрес VPN-сервера для исключения из туннеля")
	}

	// Resolve every server's physical path first: if this fails after the
	// split default is in place, xray's own uplink is blackholed and the
	// machine loses connectivity entirely.
	type exception struct{ ip, nextHop, ifIndex string }
	exceptions := make([]exception, 0, len(cfg.ServerIPs))
	for _, ip := range cfg.ServerIPs {
		// Find-NetRoute emits both the route and the source IP address object,
		// in no guaranteed order; only the former carries NextHop.
		out, err := powershell(fmt.Sprintf(
			`$r = Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop | Where-Object NextHop | Select-Object -First 1; "$($r.NextHop) $($r.InterfaceIndex)"`,
			ip))
		if err != nil {
			return fmt.Errorf("определить маршрут до сервера %s: %w\n%s", ip, err, out)
		}
		nextHop, physIndex, err := parseFindNetRoute(string(out))
		if err != nil {
			return fmt.Errorf("разобрать маршрут до сервера %s: %w", ip, err)
		}
		exceptions = append(exceptions, exception{ip: ip, nextHop: nextHop, ifIndex: physIndex})
	}

	out, err := powershell(fmt.Sprintf(`(Get-NetAdapter -Name '%s' -ErrorAction Stop).ifIndex`, cfg.Iface))
	if err != nil {
		return fmt.Errorf("найти адаптер %s: %w\n%s", cfg.Iface, err, out)
	}
	tunIndex := strings.TrimSpace(string(out))
	if tunIndex == "" {
		return fmt.Errorf("адаптер %s не имеет индекса интерфейса", cfg.Iface)
	}

	saved := cfg
	installed = &saved

	for _, e := range exceptions {
		if out, err := ipCmd.run("route", "add", e.ip, "mask", "255.255.255.255",
			e.nextHop, "if", e.ifIndex); err != nil {
			DisableTunRouting()
			return fmt.Errorf("исключить сервер %s из туннеля: %w\n%s", e.ip, err, out)
		}
	}

	for _, half := range splitDefault {
		if out, err := ipCmd.run("route", "add", half.dest, "mask", half.mask,
			cfg.Addr, "if", tunIndex); err != nil {
			DisableTunRouting()
			return fmt.Errorf("направить трафик в %s: %w\n%s", cfg.Iface, err, out)
		}
	}

	slog.Info("tun routing enabled", "iface", cfg.Iface, "excluded_servers", len(exceptions), "tun_if", tunIndex)
	return nil
}

// DisableTunRouting removes the routes EnableTunRouting installed, restoring
// the physical default path.
func DisableTunRouting() error {
	if installed == nil {
		return nil
	}

	for _, half := range splitDefault {
		ipCmd.run("route", "delete", half.dest, "mask", half.mask)
	}
	for _, ip := range installed.ServerIPs {
		ipCmd.run("route", "delete", ip, "mask", "255.255.255.255")
	}
	installed = nil

	slog.Info("tun routing disabled")
	return nil
}
