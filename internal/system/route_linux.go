//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"xray-runner/internal/xraycfg"
)

// splitDefault covers the whole IPv4 space in two halves. Each is more
// specific than the existing 0.0.0.0/0, so it wins by prefix length without
// deleting the physical default route — which stays available for the
// server exception below and for a clean teardown.
var splitDefault = []string{"0.0.0.0/1", "128.0.0.0/1"}

// directTable holds a copy of the physical default route. Packets carrying
// xraycfg.DirectFwMark — everything xray sends out a freedom outbound — are
// steered here instead of the split default, so traffic the panel routes
// `direct` leaves through the real uplink rather than looping back into the tun.
const directTable = "8888"

// markProbe is the destination used to learn the physical default path. The
// server exceptions cannot stand in for it: a server on the local link has no
// gateway, which says nothing about how the host reaches the internet.
const markProbe = "1.1.1.1"

// ipCmd is overridable in tests.
var ipCmd commander = execCommander{}

// installed remembers what EnableTunRouting added so teardown removes exactly
// that — and nothing that belongs to another VPN client on the host.
var installed *TunRouteConfig

// DirectBind reports how freedom outbounds leave the tunnel on this platform.
// Linux marks the sockets; EnableTunRouting installs the matching ip rule.
func DirectBind() (xraycfg.DirectBind, error) {
	return xraycfg.DirectBind{Mark: xraycfg.DirectFwMark}, nil
}

// parseRouteGet extracts the gateway and device from `ip route get` output,
// e.g. "45.150.32.235 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 uid 0".
// A destination on the local link has no "via" and returns an empty gateway.
func parseRouteGet(out string) (via, dev string, err error) {
	fields := strings.Fields(out)
	for i, f := range fields {
		if i+1 >= len(fields) {
			break
		}
		switch f {
		case "via":
			via = fields[i+1]
		case "dev":
			dev = fields[i+1]
		}
	}
	if dev == "" {
		return "", "", fmt.Errorf("no device in route output: %q", out)
	}
	return via, dev, nil
}

// EnableTunRouting points the system's default traffic at the TUN device and
// pins the VPN server to the physical path.
func EnableTunRouting(cfg TunRouteConfig) error {
	if err := ipCmd.lookPath("ip"); err != nil {
		return fmt.Errorf("iproute2 не найден: %w", err)
	}

	if len(cfg.ServerIPs) == 0 {
		return fmt.Errorf("не задан адрес VPN-сервера для исключения из туннеля")
	}

	// Resolve every server's physical path first: if this fails after the
	// split default is in place, xray's own uplink is blackholed and the
	// machine loses connectivity entirely.
	type exception struct{ ip, via, dev string }
	exceptions := make([]exception, 0, len(cfg.ServerIPs))
	for _, ip := range cfg.ServerIPs {
		out, err := ipCmd.run("ip", "route", "get", ip)
		if err != nil {
			return fmt.Errorf("определить маршрут до сервера %s: %w\n%s", ip, err, out)
		}
		via, dev, err := parseRouteGet(string(out))
		if err != nil {
			return fmt.Errorf("разобрать маршрут до сервера %s: %w", ip, err)
		}
		exceptions = append(exceptions, exception{ip: ip, via: via, dev: dev})
	}

	// The physical path for marked traffic is resolved here, alongside the
	// server exceptions, for the same reason: after the split default is in
	// place `ip route get` answers with the tun device.
	out, err := ipCmd.run("ip", "route", "get", markProbe)
	if err != nil {
		return fmt.Errorf("определить физический маршрут по умолчанию: %w\n%s", err, out)
	}
	directVia, directDev, err := parseRouteGet(string(out))
	if err != nil {
		return fmt.Errorf("разобрать физический маршрут по умолчанию: %w", err)
	}

	saved := cfg
	installed = &saved

	// Every install below is idempotent: a crash never runs the teardown, so the
	// next start finds its own routes still in place. `route add` would fail
	// there with "File exists" and leave the user stranded until they flush the
	// table by hand (L-1).
	for _, e := range exceptions {
		args := []string{"route", "replace", e.ip + "/32"}
		if e.via != "" {
			args = append(args, "via", e.via)
		}
		args = append(args, "dev", e.dev)

		if out, err := ipCmd.run("ip", args...); err != nil {
			_ = DisableTunRouting()
			return fmt.Errorf("исключить сервер %s из туннеля: %w\n%s", e.ip, err, out)
		}
	}

	// Marked traffic gets its escape hatch before the split default exists,
	// otherwise direct connections loop during the gap between the two.
	directRoute := []string{"route", "replace", "default"}
	if directVia != "" {
		directRoute = append(directRoute, "via", directVia)
	}
	directRoute = append(directRoute, "dev", directDev, "table", directTable)
	if out, err := ipCmd.run("ip", directRoute...); err != nil {
		_ = DisableTunRouting()
		return fmt.Errorf("проложить прямой маршрут мимо туннеля: %w\n%s", err, out)
	}
	// ip rule has no `replace`, and `add` stacks duplicates the teardown only
	// removes one of — so clear a stale copy first. The del is best-effort:
	// on a clean start there is nothing to remove.
	markRule := []string{"rule", "fwmark", strconv.Itoa(xraycfg.DirectFwMark), "lookup", directTable}
	_, _ = ipCmd.run("ip", append([]string{"rule", "del"}, markRule[1:]...)...)
	if out, err := ipCmd.run("ip", append([]string{"rule", "add"}, markRule[1:]...)...); err != nil {
		_ = DisableTunRouting()
		return fmt.Errorf("вывести прямой трафик из туннеля: %w\n%s", err, out)
	}

	for _, half := range splitDefault {
		if out, err := ipCmd.run("ip", "route", "replace", half, "dev", cfg.Iface); err != nil {
			_ = DisableTunRouting()
			return fmt.Errorf("направить трафик в %s: %w\n%s", cfg.Iface, err, out)
		}
	}

	slog.Info("tun routing enabled", "iface", cfg.Iface, "excluded_servers", len(exceptions))
	return nil
}

// DisableTunRouting removes the routes EnableTunRouting installed, restoring
// the physical default path.
func DisableTunRouting() error {
	if installed == nil {
		return nil
	}
	if err := ipCmd.lookPath("ip"); err != nil {
		return nil
	}

	for _, half := range splitDefault {
		_, _ = ipCmd.run("ip", "route", "del", half, "dev", installed.Iface)
	}
	_, _ = ipCmd.run("ip", "rule", "del", "fwmark", strconv.Itoa(xraycfg.DirectFwMark), "lookup", directTable)
	_, _ = ipCmd.run("ip", "route", "flush", "table", directTable)
	for _, ip := range installed.ServerIPs {
		_, _ = ipCmd.run("ip", "route", "del", ip+"/32")
	}
	installed = nil

	slog.Info("tun routing disabled")
	return nil
}
