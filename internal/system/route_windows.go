//go:build windows

package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
var splitDefault = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}

// splitDefault6 is the IPv6 counterpart of splitDefault: two halves that beat
// the existing ::/0 on prefix length without deleting it.
var splitDefault6 = []netip.Prefix{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1")}

// ipCmd is overridable in tests.
var ipCmd commander = execCommander{}

// tunEntry is one route EnableTunRouting added. Windows keys a route by its
// prefix, interface and next hop — the metric is not part of the key — so
// these three name the entry, and its removal touches no other route.
type tunEntry struct {
	what    string // what the route is for, for the error when it cannot be added
	prefix  netip.Prefix
	ifIndex int
	nextHop netip.Addr // unspecified for an on-link route
	// selfHop marks a next hop that is the interface's own address, which
	// Windows may keep as an on-link route instead.
	selfHop bool
}

// installed lists what EnableTunRouting added, oldest first. A route joins
// once its add went through and leaves once it is gone from the host, so a
// failed teardown keeps it for the next attempt. The interface indexes in it
// belong to one adapter generation: a full teardown empties it, and the next
// Enable looks everything up afresh.
var installed []tunEntry

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
		return xraycfg.DirectBind{}, fmt.Errorf("определить физический адаптер: %w: %w\n%s", ErrNoRoute, err, out)
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
//
// The script is shell text, not an argument vector, and gosec does not see into
// it: every value spliced in must be a constant, an integer or a netip value,
// never a string read from outside.
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

// activeRoutes reads the active routing table.
func activeRoutes() ([]winRoute, error) {
	routes, err := psJSON[winRoute](
		`Get-NetRoute -PolicyStore ActiveStore -ErrorAction Stop | Select-Object -Property DestinationPrefix,NextHop,InterfaceIndex | ConvertTo-Json -Compress`)
	if err != nil {
		return nil, fmt.Errorf("прочитать таблицу маршрутов: %w", err)
	}
	return routes, nil
}

// EnableTunRouting points the system's default traffic at the TUN adapter and
// pins the VPN server to the physical adapter. It only adds: a route already
// in place is refused before the first change, and a failure takes back
// exactly what this call added.
func EnableTunRouting(cfg TunRouteConfig) error {
	if len(cfg.ServerIPs) == 0 {
		return fmt.Errorf("не задан адрес VPN-сервера для исключения из туннеля")
	}
	servers, err := serverAddrs(cfg.ServerIPs, false)
	if err != nil {
		return err
	}
	tunAddr, err := netip.ParseAddr(cfg.Addr)
	if err != nil || !tunAddr.Is4() {
		return fmt.Errorf("адрес TUN %q — не IPv4-адрес", cfg.Addr)
	}
	var servers6 []netip.Addr
	var tunAddr6 netip.Addr
	if cfg.Addr6 != "" {
		if servers6, err = serverAddrs(cfg.ServerIPs6, true); err != nil {
			return err
		}
		if tunAddr6, err = netip.ParseAddr(cfg.Addr6); err != nil || !tunAddr6.Is6() {
			return fmt.Errorf("адрес TUN %q — не IPv6-адрес", cfg.Addr6)
		}
	}

	// A teardown that failed left its routes owned: finish it before building
	// a second set on top.
	if err := DisableTunRouting(); err != nil {
		return err
	}

	// Resolve every server's physical path first: if this fails after the
	// split default is in place, xray's own uplink is blackholed and the
	// machine loses connectivity entirely.
	var want, want6 []tunEntry
	for _, ip := range servers {
		e, err := physicalPath(ip)
		if err != nil {
			return err
		}
		want = append(want, e)
	}
	for _, ip := range servers6 {
		e, err := physicalPath(ip)
		if err != nil {
			return err
		}
		want6 = append(want6, e)
	}

	adapters, err := psJSON[struct{ IfIndex int }](fmt.Sprintf(
		`Get-NetAdapter -Name '%s' -ErrorAction Stop | Select-Object -Property ifIndex | ConvertTo-Json -Compress`, cfg.Iface))
	if err != nil {
		return fmt.Errorf("найти адаптер %s: %w", cfg.Iface, err)
	}
	if len(adapters) != 1 || adapters[0].IfIndex <= 0 {
		return fmt.Errorf("не удалось определить индекс интерфейса адаптера %s", cfg.Iface)
	}
	tunIndex := adapters[0].IfIndex

	// The exceptions go before the halves of their family, so the uplink is
	// never blackholed in between.
	for _, half := range splitDefault {
		want = append(want, tunEntry{what: "направить трафик в " + cfg.Iface,
			prefix: half, ifIndex: tunIndex, nextHop: tunAddr, selfHop: true})
	}
	// Without the IPv6 half a v6-capable app prefers the AAAA record and leaves
	// with its real address while the screen says the tunnel is up — the leak
	// the split mode exists to prevent.
	if cfg.Addr6 != "" {
		for _, half := range splitDefault6 {
			want6 = append(want6, tunEntry{what: "направить IPv6-трафик в " + cfg.Iface,
				prefix: half, ifIndex: tunIndex, nextHop: tunAddr6, selfHop: true})
		}
	}
	want = append(want, want6...)

	// Nothing here may overwrite a route that is already there (A08), so the
	// whole set is checked before the first change.
	if err := checkTunRoutingFree(want); err != nil {
		return err
	}

	// An add fails on a route a client slipped in after the check instead of
	// replacing it, and a route is owned only once its add went through. The
	// journal hears of it before the add: a run killed right after the add
	// still has the route written down (H06).
	for _, e := range want {
		journalRoutes(append(slices.Clip(installed), e))
		if out, err := e.add(); err != nil {
			err = fmt.Errorf("%s: %w\n%s", e.what, err, out)
			if undoErr := DisableTunRouting(); undoErr != nil {
				err = fmt.Errorf("%w\nоткат не завершён: %w", err, undoErr)
			}
			return err
		}
		installed = append(installed, e)
	}

	slog.Info("tun routing enabled", "iface", cfg.Iface, "excluded_servers", len(servers), "tun_if", tunIndex, "ipv6", cfg.Addr6 != "")
	return nil
}

// add installs the entry: route.exe for IPv4, netsh for IPv6. Both refuse a
// route whose key is already there instead of replacing it.
func (e tunEntry) add() ([]byte, error) {
	index := strconv.Itoa(e.ifIndex)
	if e.prefix.Addr().Is4() {
		mask := net.IP(net.CIDRMask(e.prefix.Bits(), 32)).String()
		return ipCmd.run("route", "add", e.prefix.Addr().String(), "mask", mask, e.nextHop.String(), "if", index)
	}
	// An on-link destination has no next hop (Find-NetRoute answers "::"), and
	// netsh wants the parameter left out rather than set to the unspecified
	// address.
	args := []string{"interface", "ipv6", "add", "route", "prefix=" + e.prefix.String(), "interface=" + index, "store=active"}
	if !e.nextHop.IsUnspecified() {
		args = append(args, "nexthop="+e.nextHop.String())
	}
	return ipCmd.run("netsh", args...)
}

// find looks the entry up in the active table and returns its next hop as the
// table holds it. present means it is still there; foreign means it is gone
// but another route now holds its prefix, which the teardown must leave alone.
func (e tunEntry) find(routes []winRoute) (hop netip.Addr, present, foreign bool, err error) {
	for _, r := range routes {
		p, err := netip.ParsePrefix(r.DestinationPrefix)
		if err != nil {
			return netip.Addr{}, false, false, fmt.Errorf("разобрать назначение маршрута %q: %w", r.DestinationPrefix, err)
		}
		if p.Masked() != e.prefix {
			continue
		}
		h, err := netip.ParseAddr(r.NextHop)
		if err != nil {
			return netip.Addr{}, false, false, fmt.Errorf("разобрать next hop маршрута %s: %w", p, err)
		}
		if r.InterfaceIndex == e.ifIndex && (h == e.nextHop || e.selfHop && h.IsUnspecified()) {
			return h, true, false, nil
		}
		foreign = true
	}
	return netip.Addr{}, false, foreign, nil
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

// physicalPath asks Windows how it reaches ip right now and returns the
// exception that keeps it there. Find-NetRoute emits both the route and the
// source IP address object, in no guaranteed order; only the former carries
// NextHop.
func physicalPath(ip netip.Addr) (tunEntry, error) {
	found, err := psJSON[winRoute](fmt.Sprintf(
		`Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop | Where-Object NextHop | Select-Object -First 1 -Property NextHop,InterfaceIndex | ConvertTo-Json -Compress`,
		ip))
	if err != nil {
		return tunEntry{}, fmt.Errorf("определить маршрут до сервера %s: %w: %w", ip, ErrNoRoute, err)
	}
	r := found[0]
	hop, err := netip.ParseAddr(r.NextHop)
	if len(found) != 1 || err != nil || hop.Is4() != ip.Is4() || r.InterfaceIndex <= 0 {
		return tunEntry{}, fmt.Errorf("разобрать маршрут до сервера %s: next hop %q, интерфейс %d", ip, r.NextHop, r.InterfaceIndex)
	}
	return tunEntry{what: "исключить сервер " + ip.String() + " из туннеля",
		prefix: netip.PrefixFrom(ip, ip.BitLen()), ifIndex: r.InterfaceIndex, nextHop: hop}, nil
}

// checkTunRoutingFree refuses to route while a route with the prefix of any
// entry of want is already in the active table, whatever its interface, next
// hop or metric. A crashed run's leftovers look exactly like another client's
// routes, so a match proves nothing about ownership and is never taken over.
// Only exact prefixes count: the default routes, the LAN and other clients'
// more specific routes are no conflict. A query that fails or prints something
// unreadable refuses too — routing on a guess is how foreign routes got
// overwritten.
func checkTunRoutingFree(want []tunEntry) error {
	routes, err := activeRoutes()
	if err != nil {
		return err
	}
	var conflicts []string
	for _, r := range routes {
		p, err := netip.ParsePrefix(r.DestinationPrefix)
		if err != nil {
			return fmt.Errorf("разобрать назначение маршрута %q: %w", r.DestinationPrefix, err)
		}
		if slices.ContainsFunc(want, func(e tunEntry) bool { return e.prefix == p.Masked() }) {
			conflicts = append(conflicts, fmt.Sprintf("%s via %s if %d", p, r.NextHop, r.InterfaceIndex))
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("уже есть маршруты, которые ставит TUN: %s — чужое не перезаписываю, ничего не изменено (остатки упавшего запуска удалите вручную или перезагрузкой)",
			strings.Join(conflicts, "; "))
	}
	return nil
}

// DisableTunRouting removes what EnableTunRouting installed, newest first, so
// the split default goes before the exceptions it relies on. Each route is
// looked up before it is removed: one the adapter took with it is already
// gone, and one another client has since replaced is not ours to remove. A
// route that could not be removed stays owned and the error says so; the next
// call retries it.
func DisableTunRouting() error {
	if len(installed) == 0 {
		return nil
	}
	routes, err := activeRoutes()
	if err != nil {
		return fmt.Errorf("маршруты TUN не сняты: %w", err)
	}

	var present []tunEntry
	for _, e := range slices.Backward(installed) {
		hop, ok, foreign, err := e.find(routes)
		if err != nil {
			return fmt.Errorf("маршруты TUN не сняты: %w", err)
		}
		if foreign {
			slog.Warn("tun route replaced by another client, left in place", "prefix", e.prefix.String(), "if", e.ifIndex)
		}
		if ok {
			e.nextHop = hop
			present = append(present, e)
		}
	}

	if len(present) > 0 {
		if out, err := powershell(removeScript(present)); err != nil {
			return keepUnremoved(present, fmt.Errorf("%w\n%s", err, out))
		}
	}
	installed = nil
	journalRoutes(nil)

	slog.Info("tun routing disabled")
	return nil
}

// removeScript removes each route by its full key, in the order given. Stop
// turns a failed removal into a failed script — the only way the caller hears
// of it — and leaves the removals after it for the next attempt.
func removeScript(entries []tunEntry) string {
	cmds := []string{"$ErrorActionPreference = 'Stop'"}
	for _, e := range entries {
		cmds = append(cmds, fmt.Sprintf(
			"Remove-NetRoute -DestinationPrefix '%s' -InterfaceIndex %d -NextHop '%s' -PolicyStore ActiveStore -Confirm:$false",
			e.prefix, e.ifIndex, e.nextHop))
	}
	return strings.Join(cmds, "; ")
}

// keepUnremoved ends a teardown whose removal script failed part way: the
// routes of present, newest first, that the table still holds stay owned. When
// the table cannot be read either, all of them do.
func keepUnremoved(present []tunEntry, cause error) error {
	kept := slices.Clone(present)
	if routes, err := activeRoutes(); err == nil {
		kept = slices.DeleteFunc(kept, func(e tunEntry) bool {
			_, ok, _, err := e.find(routes)
			return err == nil && !ok
		})
	} else {
		cause = errors.Join(cause, err)
	}
	slices.Reverse(kept)
	installed = kept
	journalRoutes(installed)
	return fmt.Errorf("маршруты TUN сняты не полностью: %w", cause)
}
