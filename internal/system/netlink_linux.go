//go:build linux

package system

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"xray-runner/internal/xraycfg"
)

// netlinkOps is netOps over rtnetlink. It needs CAP_NET_ADMIN for the changes,
// which root has and so has a binary given it with setcap (H07): the program
// talks to the kernel itself, so no child has to inherit the capability.
type netlinkOps struct{}

func (netlinkOps) routeGet(dst netip.Addr) (via, dev string, err error) {
	routes, err := netlink.RouteGet(net.IP(dst.AsSlice()))
	if err != nil {
		return "", "", err
	}
	if len(routes) == 0 {
		return "", "", fmt.Errorf("ядро не вернуло маршрут до %s", dst)
	}
	r := routes[0]
	link, err := netlink.LinkByIndex(r.LinkIndex)
	if err != nil {
		return "", "", fmt.Errorf("интерфейс маршрута до %s: %w", dst, err)
	}
	if r.Gw != nil {
		via = r.Gw.String()
	}
	return via, link.Attrs().Name, nil
}

func (netlinkOps) listRoutes(v6 bool) ([]ipRoute, error) {
	family := netlink.FAMILY_V4
	if v6 {
		family = netlink.FAMILY_V6
	}
	// Table unspecified with the table filter on is every table; the library
	// otherwise keeps to main.
	list, err := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("прочитать маршруты: %w", err)
	}
	names, err := linkNames()
	if err != nil {
		return nil, err
	}
	out := make([]ipRoute, 0, len(list))
	for _, r := range list {
		out = append(out, toIPRoute(r, v6, names))
	}
	return out, nil
}

// toIPRoute spells a kernel route the way ipRoute keeps it.
func toIPRoute(r netlink.Route, v6 bool, names map[int]string) ipRoute {
	out := ipRoute{Dev: names[r.LinkIndex], Metric: r.Priority}
	switch {
	case r.Dst == nil:
		out.Dst = "default"
	default:
		ones, bits := r.Dst.Mask.Size()
		addr, ok := netip.AddrFromSlice(r.Dst.IP)
		if !ok {
			out.Dst = r.Dst.String()
			break
		}
		if !v6 {
			addr = addr.Unmap()
		}
		switch {
		case ones == 0 && bits > 0:
			out.Dst = "default"
		case ones == bits:
			out.Dst = addr.String()
		default:
			out.Dst = netip.PrefixFrom(addr, ones).String()
		}
	}
	if r.Gw != nil {
		out.Gateway = r.Gw.String()
	}
	if r.Table != unix.RT_TABLE_MAIN {
		out.Table = strconv.Itoa(r.Table)
	}
	if r.Type != unix.RTN_UNICAST {
		out.Type = strconv.Itoa(r.Type)
	}
	return out
}

// linkNames maps interface indexes to names, for the routes' devices.
func linkNames() (map[int]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("прочитать интерфейсы: %w", err)
	}
	names := make(map[int]string, len(links))
	for _, l := range links {
		names[l.Attrs().Index] = l.Attrs().Name
	}
	return names, nil
}

func (netlinkOps) listRules() ([]ipRule, error) {
	list, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("прочитать правила маршрутизации: %w", err)
	}
	out := make([]ipRule, 0, len(list))
	for _, r := range list {
		ir := ipRule{Priority: r.Priority, Table: strconv.Itoa(r.Table)}
		if r.Mark != 0 || r.Mask != nil {
			ir.FwMark = fmt.Sprintf("%#x", r.Mark)
		}
		if r.Mask != nil && *r.Mask != 0xffffffff {
			ir.FwMask = fmt.Sprintf("%#x", *r.Mask)
		}
		out = append(out, ir)
	}
	return out, nil
}

func (netlinkOps) linkAddrs(dev string) ([]netip.Addr, error) {
	link, err := netlink.LinkByName(dev)
	if err != nil {
		return nil, err
	}
	list, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(list))
	for _, a := range list {
		if addr, ok := netip.AddrFromSlice(a.IP); ok {
			out = append(out, addr.Unmap())
		}
	}
	return out, nil
}

func (netlinkOps) add(e tunEntry) error {
	if e.rule {
		return netlink.RuleAdd(e.nlRule())
	}
	r, err := e.nlRoute(false)
	if err != nil {
		return err
	}
	return netlink.RouteAdd(r)
}

func (netlinkOps) del(e tunEntry) error {
	if e.rule {
		return netlink.RuleDel(e.nlRule())
	}
	r, err := e.nlRoute(true)
	if err != nil {
		return err
	}
	return netlink.RouteDel(r)
}

// nlRule is the mark rule: fwmark DirectFwMark with no mask — the kernel then
// compares all 32 bits — to the direct table, at the entry's priority.
func (e tunEntry) nlRule() *netlink.Rule {
	r := netlink.NewRule()
	r.Family = netlink.FAMILY_V4
	r.Priority = e.pref
	r.Mark = xraycfg.DirectFwMark
	r.Table = mustTable(directTable)
	return r
}

// nlRoute builds the route as ip would send it. On an add a unicast route with
// no gateway is scoped to the link; a delete names the scope as "any"
// (RT_SCOPE_NOWHERE) and the type only when the entry has one, as ip does, so
// it matches exactly the selectors the entry spells out, the metric among
// them (F02).
func (e tunEntry) nlRoute(del bool) (*netlink.Route, error) {
	r := &netlink.Route{
		Dst:      &net.IPNet{IP: e.prefix.Addr().AsSlice(), Mask: net.CIDRMask(e.prefix.Bits(), e.prefix.Addr().BitLen())},
		Priority: e.metric,
		Family:   netlink.FAMILY_V4,
	}
	if e.v6 {
		r.Family = netlink.FAMILY_V6
	}
	if e.table != "" {
		r.Table = mustTable(e.table)
	}
	if e.dev != "" {
		link, err := netlink.LinkByName(e.dev)
		if err != nil {
			return nil, fmt.Errorf("интерфейс %s: %w", e.dev, err)
		}
		r.LinkIndex = link.Attrs().Index
	}
	if e.via != "" {
		gw, err := netip.ParseAddr(e.via)
		if err != nil {
			return nil, fmt.Errorf("шлюз %q: %w", e.via, err)
		}
		r.Gw = gw.AsSlice()
	}
	switch e.typ {
	case "":
		if !del {
			r.Type = unix.RTN_UNICAST
		}
	case "unreachable":
		r.Type = unix.RTN_UNREACHABLE
	default:
		return nil, errors.New("неизвестный тип маршрута " + e.typ)
	}
	switch {
	case del:
		r.Scope = unix.RT_SCOPE_NOWHERE
	case e.typ == "" && e.via == "":
		r.Scope = unix.RT_SCOPE_LINK
	default:
		r.Scope = unix.RT_SCOPE_UNIVERSE
	}
	return r, nil
}

func mustTable(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic("table " + s + " is not a number")
	}
	return n
}
