//go:build linux

package system

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
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

// blockDefault6 covers the whole IPv6 space in two halves, the same way
// splitDefault does for IPv4.
var blockDefault6 = []string{"::/1", "8000::/1"}

// ifInet6 lists the host's IPv6 addresses and is missing when the kernel runs
// without IPv6 at all. Overridable in tests.
var ifInet6 = "/proc/net/if_inet6"

// hasIPv6Stack reports whether the kernel has IPv6: without it there is nothing
// to leak and no table to write the block into.
func hasIPv6Stack() bool {
	_, err := os.Stat(ifInet6)
	return err == nil
}

// ipCmd is overridable in tests.
var ipCmd commander = execCommander{}

// installed remembers what EnableTunRouting added so teardown touches only
// those destinations, leaving the rest of the host's table alone. The one gap:
// a client that routes one of them after checkTunRoutingFree has its entry
// replaced, and the teardown deletes the destination outright.
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

	// Nothing below may overwrite an entry that is already there (A08), so the
	// whole set is checked before the first change.
	block6 := cfg.Addr6 != "" && hasIPv6Stack()
	if err := checkTunRoutingFree(cfg, block6); err != nil {
		return err
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

	// checkTunRoutingFree refused anything already in place, a crashed run's
	// leftovers included, so replace below finds nothing to overwrite unless a
	// client added an entry after the check.
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

	// A11: the VPN runs without IPv6, so tun does not route it into the tunnel —
	// it refuses it, before IPv4 is captured. Unreachable rather than blackhole:
	// the app hears at once and falls back to IPv4, which the tunnel carries,
	// instead of hanging until a timeout. Link-local and LAN prefixes have
	// routes of their own, more specific than these halves, and keep working.
	if block6 {
		for _, half := range blockDefault6 {
			if out, err := ipCmd.run("ip", "-6", "route", "replace", "unreachable", half); err != nil {
				_ = DisableTunRouting()
				return fmt.Errorf("закрыть IPv6 мимо туннеля: %w\n%s", err, out)
			}
		}
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
	if installed.Addr6 != "" && hasIPv6Stack() {
		for _, half := range blockDefault6 {
			_, _ = ipCmd.run("ip", "-6", "route", "del", "unreachable", half)
		}
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

// checkTunRoutingFree refuses to route while any entry EnableTunRouting would
// create is already there. A crashed run's leftovers match another client's
// byte for byte, so a match proves nothing about ownership and is never taken
// over. Only exact prefixes count: the default route, the LAN and other
// clients' more specific routes are no conflict. A query that fails or prints
// something unreadable refuses too — routing on a guess is how foreign entries
// got overwritten.
func checkTunRoutingFree(cfg TunRouteConfig, block6 bool) error {
	// xray assigns the address before it brings the interface up, so a tun
	// without it is not the one this core created.
	var links []ipLink
	if err := ipJSON(&links, "addr", "show", "dev", cfg.Iface); err != nil {
		return err
	}
	var hasAddr bool
	for _, l := range links {
		for _, a := range l.AddrInfo {
			hasAddr = hasAddr || a.Family == "inet" && a.Local == cfg.Addr
		}
	}
	if !hasAddr {
		return fmt.Errorf("у %s нет адреса %s — это не интерфейс, который поднимает ядро", cfg.Iface, cfg.Addr)
	}

	own := map[netip.Prefix]bool{}
	for _, half := range splitDefault {
		own[netip.MustParsePrefix(half)] = true
	}
	for _, ip := range cfg.ServerIPs {
		p, err := routeDst(ip)
		if err != nil {
			return err
		}
		own[p] = true
	}
	families := []string{"-4"}
	if block6 {
		for _, half := range blockDefault6 {
			own[netip.MustParsePrefix(half)] = true
		}
		families = append(families, "-6")
	}

	var conflicts []string
	for _, family := range families {
		var routes []ipRoute
		if err := ipJSON(&routes, family, "route", "show", "table", "all"); err != nil {
			return err
		}
		for _, r := range routes {
			if family == "-4" && r.Table == directTable {
				conflicts = append(conflicts, "таблица "+directTable+": "+r.describe(r.Dst))
				continue
			}
			// Every route EnableTunRouting adds lands in the main table, and
			// none of them is a default.
			if r.Table != "" || r.Dst == "default" {
				continue
			}
			p, err := routeDst(r.Dst)
			if err != nil {
				return err
			}
			if own[p] {
				conflicts = append(conflicts, r.describe(p.String()))
			}
		}
	}

	var rules []ipRule
	if err := ipJSON(&rules, "-4", "rule", "show"); err != nil {
		return err
	}
	for _, r := range rules {
		catches, err := r.catchesDirectMark()
		if err != nil {
			return err
		}
		if catches || r.Table == directTable {
			conflicts = append(conflicts, r.describe())
		}
	}

	if len(conflicts) > 0 {
		return fmt.Errorf("уже есть записи, которые ставит TUN: %s — чужое не перезаписываю, ничего не изменено (остатки упавшего запуска удалите вручную или перезагрузкой)",
			strings.Join(conflicts, "; "))
	}
	return nil
}

// ipJSON runs a read-only `ip -j -N` query and decodes its output. -N keeps
// table numbers numeric: a name given to 8888 in rt_tables would hide it.
func ipJSON(v any, args ...string) error {
	query := strings.Join(args, " ")
	out, err := ipCmd.run("ip", append([]string{"-j", "-N"}, args...)...)
	if err != nil {
		return fmt.Errorf("прочитать состояние сети (ip %s): %w\n%s", query, err, out)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("разобрать вывод ip %s: %w", query, err)
	}
	return nil
}

// ipLink is one entry of `ip -j addr show`, reduced to its addresses.
type ipLink struct {
	AddrInfo []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
	} `json:"addr_info"`
}

// ipRoute is one entry of `ip -j -N route show`, reduced to what the preflight
// reads. A main-table route carries no "table" key.
type ipRoute struct {
	Dst     string `json:"dst"`
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	Table   string `json:"table"`
}

func (r ipRoute) describe(dst string) string {
	if r.Gateway != "" {
		dst += " via " + r.Gateway
	}
	if r.Dev != "" {
		dst += " dev " + r.Dev
	}
	return dst
}

// routeDst reads a destination the way ip prints it: a host route drops its
// /32 or /128.
func routeDst(dst string) (netip.Prefix, error) {
	if addr, err := netip.ParseAddr(dst); err == nil {
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	p, err := netip.ParsePrefix(dst)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("разобрать назначение маршрута %q: %w", dst, err)
	}
	return p, nil
}

// ipRule is one entry of `ip -j -N rule show`; fwmark and fwmask print in hex.
type ipRule struct {
	Priority int    `json:"priority"`
	FwMark   string `json:"fwmark"`
	FwMask   string `json:"fwmask"`
	Table    string `json:"table"`
}

// catchesDirectMark reports whether the rule selects packets carrying
// DirectFwMark. A fwmark without a mask compares all 32 bits.
func (r ipRule) catchesDirectMark() (bool, error) {
	if r.FwMark == "" {
		return false, nil
	}
	mark, err := strconv.ParseUint(r.FwMark, 0, 32)
	if err != nil {
		return false, fmt.Errorf("разобрать fwmark правила %d: %w", r.Priority, err)
	}
	mask := uint64(0xffffffff)
	if r.FwMask != "" {
		if mask, err = strconv.ParseUint(r.FwMask, 0, 32); err != nil {
			return false, fmt.Errorf("разобрать fwmask правила %d: %w", r.Priority, err)
		}
	}
	return (mark^xraycfg.DirectFwMark)&mask == 0, nil
}

func (r ipRule) describe() string {
	s := "правило " + strconv.Itoa(r.Priority)
	if r.FwMark != "" {
		s += " fwmark " + r.FwMark
		if r.FwMask != "" {
			s += "/" + r.FwMask
		}
	}
	if r.Table != "" {
		s += " lookup " + r.Table
	}
	return s
}
