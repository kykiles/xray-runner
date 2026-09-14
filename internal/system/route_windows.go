//go:build windows

package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

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

// installed remembers what EnableTunRouting added so teardown touches only
// those destinations, leaving the rest of the host's table alone. The one gap:
// Enable refuses a destination that is already there, but routeReplace still
// clears one another client added after that check, and the teardown deletes
// the destination outright instead of restoring it.
var installed *TunRouteConfig

// installedV6 holds the netsh argument lists that undo the IPv6 routes. They
// are recorded rather than rebuilt because deleting an IPv6 route needs the
// interface index it was added on, and by teardown time the adapter may already
// be gone — Find-NetRoute would have nothing left to answer with.
var installedV6 [][]string

// splitDefault6 is the IPv6 counterpart of splitDefault: two halves that beat
// the existing ::/0 on prefix length without deleting it.
var splitDefault6 = []string{"::/1", "8000::/1"}

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
	// A mangled alias is worse than none: xray fails net.InterfaceByName, logs it
	// at Info, and dials through the routing table — straight back into the tun.
	if !utf8.ValidString(alias) {
		return xraycfg.DirectBind{}, fmt.Errorf("имя физического адаптера пришло в неверной кодировке: %q", alias)
	}
	return xraycfg.DirectBind{Interface: alias}, nil
}

// powershell runs a script and reads its output as UTF-8. Without the encoding
// line the pipe carries the console OEM codepage (cp866 on a Russian Windows),
// so any localized adapter alias arrives as mojibake.
func powershell(script string) ([]byte, error) {
	return ipCmd.run("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding=[Text.Encoding]::UTF8; "+script)
}

// psJSON runs a read-only script that ends in ConvertTo-Json and decodes its
// output.
func psJSON[T any](script string) ([]T, error) {
	out, err := powershell(script)
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, out)
	}
	return decodePS[T](out)
}

// decodePS reads ConvertTo-Json output as a list. Windows PowerShell prints a
// lone object without the array brackets and nothing at all when no object
// came through; every query here expects an answer, so silence is an error
// rather than an empty list.
func decodePS[T any](out []byte) ([]T, error) {
	// Not seen from the encoding line above, but a byte order mark would make
	// the whole answer unreadable JSON.
	out = bytes.TrimSpace(bytes.TrimPrefix(out, []byte("\xef\xbb\xbf")))
	if len(out) == 0 {
		return nil, errors.New("PowerShell ничего не вывела")
	}
	var list []T
	var err error
	if out[0] == '[' {
		err = json.Unmarshal(out, &list)
	} else {
		var one T
		err = json.Unmarshal(out, &one)
		list = []T{one}
	}
	if err != nil {
		return nil, fmt.Errorf("разобрать вывод PowerShell: %w", err)
	}
	return list, nil
}

// winRoute is one route as Get-NetRoute and Find-NetRoute report it, reduced
// to what the routing code reads. An on-link route has NextHop 0.0.0.0 or ::.
type winRoute struct {
	DestinationPrefix string
	NextHop           string
	InterfaceIndex    int
}

// routeReplace installs a route the way `ip route replace` does on Linux.
// route.exe has no replace verb and rejects a destination already present with
// "The object already exists". Enable has already refused a destination that
// was there, so the delete only reaches a route another client added since.
func routeReplace(dest, mask, gateway, ifIndex string) ([]byte, error) {
	_, _ = ipCmd.run("route", "delete", dest, "mask", mask)
	return ipCmd.run("route", "add", dest, "mask", mask, gateway, "if", ifIndex)
}

// netshRoute6 installs an IPv6 route the way routeReplace does for IPv4: the
// delete first, because netsh refuses a duplicate prefix on the same interface.
//
// An on-link destination has no next hop (Find-NetRoute answers "::"), and
// netsh wants the parameter left out rather than set to the unspecified
// address. Every added route is recorded for the teardown.
func netshRoute6(prefix, ifIndex, nextHop string) ([]byte, error) {
	del := []string{"interface", "ipv6", "delete", "route", "prefix=" + prefix, "interface=" + ifIndex, "store=active"}
	_, _ = ipCmd.run("netsh", del...)

	add := []string{"interface", "ipv6", "add", "route", "prefix=" + prefix, "interface=" + ifIndex, "store=active"}
	if nextHop != "" && nextHop != "::" {
		add = append(add, "nexthop="+nextHop)
	}
	out, err := ipCmd.run("netsh", add...)
	if err == nil {
		installedV6 = append(installedV6, del)
	}
	return out, err
}

// tunException is a server's physical path, which its exception route pins so
// the tunnel's own uplink stays out of the tun.
type tunException struct{ ip, nextHop, ifIndex string }

// EnableTunRouting points the system's default traffic at the TUN adapter and
// pins the VPN server to the physical adapter. Every lookup, and the check
// that none of the routes it adds is there already, runs before the first
// change.
func EnableTunRouting(cfg TunRouteConfig) error {
	if len(cfg.ServerIPs) == 0 {
		return fmt.Errorf("не задан адрес VPN-сервера для исключения из туннеля")
	}
	servers, err := serverAddrs(cfg.ServerIPs, false)
	if err != nil {
		return err
	}
	var servers6 []netip.Addr
	if cfg.Addr6 != "" {
		if servers6, err = serverAddrs(cfg.ServerIPs6, true); err != nil {
			return err
		}
	}

	// Resolve every server's physical path first: if this fails after the
	// split default is in place, xray's own uplink is blackholed and the
	// machine loses connectivity entirely.
	exceptions := make([]tunException, 0, len(servers))
	for _, ip := range servers {
		e, err := physicalPath(ip)
		if err != nil {
			return err
		}
		exceptions = append(exceptions, e)
	}
	exceptions6 := make([]tunException, 0, len(servers6))
	for _, ip := range servers6 {
		e, err := physicalPath(ip)
		if err != nil {
			return err
		}
		exceptions6 = append(exceptions6, e)
	}

	adapters, err := psJSON[struct{ IfIndex int }](fmt.Sprintf(
		`Get-NetAdapter -Name '%s' -ErrorAction Stop | Select-Object -Property ifIndex | ConvertTo-Json -Compress`, cfg.Iface))
	if err != nil {
		return fmt.Errorf("найти адаптер %s: %w", cfg.Iface, err)
	}
	if len(adapters) != 1 || adapters[0].IfIndex <= 0 {
		return fmt.Errorf("не удалось определить индекс интерфейса адаптера %s", cfg.Iface)
	}
	tunIndex := strconv.Itoa(adapters[0].IfIndex)

	// Nothing here may overwrite a route that is already there (A08), so the
	// whole set is checked before the first change.
	want := make([]netip.Prefix, 0, len(servers)+len(servers6)+len(splitDefault)+len(splitDefault6))
	for _, ip := range slices.Concat(servers, servers6) {
		want = append(want, netip.PrefixFrom(ip, ip.BitLen()))
	}
	for _, half := range splitDefault {
		want = append(want, netip.PrefixFrom(netip.MustParseAddr(half.dest), 1))
	}
	if cfg.Addr6 != "" {
		for _, half := range splitDefault6 {
			want = append(want, netip.MustParsePrefix(half))
		}
	}
	if err := checkTunRoutingFree(want); err != nil {
		return err
	}

	saved := cfg
	installed = &saved

	for _, e := range exceptions {
		if out, err := routeReplace(e.ip, "255.255.255.255", e.nextHop, e.ifIndex); err != nil {
			_ = DisableTunRouting()
			return fmt.Errorf("исключить сервер %s из туннеля: %w\n%s", e.ip, err, out)
		}
	}

	for _, half := range splitDefault {
		if out, err := routeReplace(half.dest, half.mask, cfg.Addr, tunIndex); err != nil {
			_ = DisableTunRouting()
			return fmt.Errorf("направить трафик в %s: %w\n%s", cfg.Iface, err, out)
		}
	}

	if cfg.Addr6 != "" {
		if err := enableTunRouting6(cfg, exceptions6, tunIndex); err != nil {
			_ = DisableTunRouting()
			return err
		}
	}

	slog.Info("tun routing enabled", "iface", cfg.Iface, "excluded_servers", len(exceptions), "tun_if", tunIndex, "ipv6", cfg.Addr6 != "")
	return nil
}

// enableTunRouting6 pulls IPv6 into the tunnel: the server exceptions first,
// then the two default halves. Without it a v6-capable app prefers the AAAA
// record and leaves with its real address while the screen says the tunnel is
// up — the leak the split mode exists to prevent.
func enableTunRouting6(cfg TunRouteConfig, exceptions []tunException, tunIndex string) error {
	for _, e := range exceptions {
		if out, err := netshRoute6(e.ip+"/128", e.ifIndex, e.nextHop); err != nil {
			return fmt.Errorf("исключить сервер %s из туннеля: %w\n%s", e.ip, err, out)
		}
	}

	for _, half := range splitDefault6 {
		if out, err := netshRoute6(half, tunIndex, cfg.Addr6); err != nil {
			return fmt.Errorf("направить IPv6-трафик в %s: %w\n%s", cfg.Iface, err, out)
		}
	}
	return nil
}

// serverAddrs reads the server addresses of one family. They are spliced into
// PowerShell scripts, so anything but a plain address of that family is
// refused before a script runs.
func serverAddrs(ips []string, v6 bool) ([]netip.Addr, error) {
	family := "IPv4"
	if v6 {
		family = "IPv6"
	}
	addrs := make([]netip.Addr, 0, len(ips))
	for _, s := range ips {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || a.Is4() == v6 || a.Is4In6() {
			return nil, fmt.Errorf("адрес VPN-сервера %q — не %s-адрес", s, family)
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}

// physicalPath asks Windows how it reaches ip right now. Find-NetRoute emits
// both the route and the source IP address object, in no guaranteed order;
// only the former carries NextHop.
func physicalPath(ip netip.Addr) (tunException, error) {
	found, err := psJSON[winRoute](fmt.Sprintf(
		`Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop | Where-Object NextHop | Select-Object -First 1 -Property NextHop,InterfaceIndex | ConvertTo-Json -Compress`,
		ip))
	if err != nil {
		return tunException{}, fmt.Errorf("определить маршрут до сервера %s: %w", ip, err)
	}
	r := found[0]
	hop, err := netip.ParseAddr(r.NextHop)
	if len(found) != 1 || err != nil || hop.Is4() != ip.Is4() || r.InterfaceIndex <= 0 {
		return tunException{}, fmt.Errorf("разобрать маршрут до сервера %s: next hop %q, интерфейс %d", ip, r.NextHop, r.InterfaceIndex)
	}
	return tunException{ip: ip.String(), nextHop: hop.String(), ifIndex: strconv.Itoa(r.InterfaceIndex)}, nil
}

// checkTunRoutingFree refuses to route while a route with any prefix of want
// is already in the active table, whatever its interface, next hop or metric.
// A crashed run's leftovers look exactly like another client's routes, so a
// match proves nothing about ownership and is never taken over. Only exact
// prefixes count: the default routes, the LAN and other clients' more specific
// routes are no conflict. A query that fails or prints something unreadable
// refuses too — routing on a guess is how foreign routes got overwritten.
func checkTunRoutingFree(want []netip.Prefix) error {
	routes, err := psJSON[winRoute](
		`Get-NetRoute -PolicyStore ActiveStore -ErrorAction Stop | Select-Object -Property DestinationPrefix,NextHop,InterfaceIndex | ConvertTo-Json -Compress`)
	if err != nil {
		return fmt.Errorf("прочитать таблицу маршрутов: %w", err)
	}
	var conflicts []string
	for _, r := range routes {
		p, err := netip.ParsePrefix(r.DestinationPrefix)
		if err != nil {
			return fmt.Errorf("разобрать назначение маршрута %q: %w", r.DestinationPrefix, err)
		}
		if slices.Contains(want, p.Masked()) {
			conflicts = append(conflicts, fmt.Sprintf("%s via %s if %d", p, r.NextHop, r.InterfaceIndex))
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("уже есть маршруты, которые ставит TUN: %s — чужое не перезаписываю, ничего не изменено (остатки упавшего запуска удалите вручную или перезагрузкой)",
			strings.Join(conflicts, "; "))
	}
	return nil
}

// DisableTunRouting removes the routes EnableTunRouting installed, restoring
// the physical default path.
func DisableTunRouting() error {
	if installed == nil {
		return nil
	}

	// Best-effort: teardown also runs after a partial install, where some of
	// these routes were never added.
	for _, half := range splitDefault {
		_, _ = ipCmd.run("route", "delete", half.dest, "mask", half.mask)
	}
	for _, ip := range installed.ServerIPs {
		_, _ = ipCmd.run("route", "delete", ip, "mask", "255.255.255.255")
	}
	// Newest first, so the default halves go before the server exceptions they
	// were added after — the same order the IPv4 teardown above walks.
	for i := len(installedV6) - 1; i >= 0; i-- {
		_, _ = ipCmd.run("netsh", installedV6[i]...)
	}
	installedV6 = nil
	installed = nil

	slog.Info("tun routing disabled")
	return nil
}
