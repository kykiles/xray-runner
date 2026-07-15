//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"strings"
)

// splitDefault covers the whole IPv4 space in two halves. Each is more
// specific than the existing 0.0.0.0/0, so it wins by prefix length without
// deleting the physical default route — which stays available for the
// server exception below and for a clean teardown.
var splitDefault = []string{"0.0.0.0/1", "128.0.0.0/1"}

// ipCmd is overridable in tests.
var ipCmd commander = execCommander{}

// installed remembers what EnableTunRouting added so teardown removes exactly
// that — and nothing that belongs to another VPN client on the host.
var installed *TunRouteConfig

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

	saved := cfg
	installed = &saved

	for _, e := range exceptions {
		args := []string{"route", "add", e.ip + "/32"}
		if e.via != "" {
			args = append(args, "via", e.via)
		}
		args = append(args, "dev", e.dev)

		if out, err := ipCmd.run("ip", args...); err != nil {
			DisableTunRouting()
			return fmt.Errorf("исключить сервер %s из туннеля: %w\n%s", e.ip, err, out)
		}
	}

	for _, half := range splitDefault {
		if out, err := ipCmd.run("ip", "route", "add", half, "dev", cfg.Iface); err != nil {
			DisableTunRouting()
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
		ipCmd.run("ip", "route", "del", half, "dev", installed.Iface)
	}
	for _, ip := range installed.ServerIPs {
		ipCmd.run("ip", "route", "del", ip+"/32")
	}
	installed = nil

	slog.Info("tun routing disabled")
	return nil
}
