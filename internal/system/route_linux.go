//go:build linux

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
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

// tunMetric is the metric every IPv4 route of ours carries. An IPv4 route added
// without one lands at metric 0, and `ip route del` given no metric matches a
// route of any metric: once another client had replaced ours, that delete took
// theirs instead (F02). Metric 0 is no fix — for fib_nh_match it is a wildcard,
// not a value. Spelling one metric out on the add and the delete alike makes
// the delete name exactly the route this process installed. It is an
// implementation constant, not a setting, and not the priority of an ip rule.
const tunMetric = 1024

// blockDefault6 covers the whole IPv6 space in two halves, the same way
// splitDefault does for IPv4.
var blockDefault6 = []string{"::/1", "8000::/1"}

// ifInet6 lists the host's IPv6 addresses and is missing when the kernel runs
// without IPv6 at all. Overridable in tests.
var ifInet6 = "/proc/net/if_inet6"

// HasIPv6Stack reports whether the kernel has IPv6. A kernel booted with
// ipv6.disable=1 has none: nothing to leak, no table to write the block into,
// and no address the tun interface could take — the core fails to come up on
// one it is given.
func HasIPv6Stack() bool {
	_, err := os.Stat(ifInet6)
	return err == nil
}

// netOps is the host's routing state, read and changed the way EnableTunRouting
// and its teardown need it. The real one speaks rtnetlink (netlink_linux.go):
// nothing is parsed out of `ip`, which the program no longer runs (H09).
type netOps interface {
	// routeGet is the path the kernel would take to dst: the gateway, empty on
	// the local link, and the device.
	routeGet(dst netip.Addr) (via, dev string, err error)
	// listRoutes lists one family's routes in every table, as ip would print them.
	listRoutes(v6 bool) ([]ipRoute, error)
	// listRules lists the IPv4 policy rules.
	listRules() ([]ipRule, error)
	// linkAddrs lists the addresses on a device.
	linkAddrs(dev string) ([]netip.Addr, error)
	// add installs the entry and fails when the kernel already has one in its
	// place; del removes exactly it.
	add(e tunEntry) error
	del(e tunEntry) error
}

// ipOps is overridable in tests.
var ipOps netOps = netlinkOps{}

// tunEntry is one route or rule EnableTunRouting added, spelled the way the
// teardown has to name it: its delete removes this entry and never a foreign
// one sharing the prefix.
type tunEntry struct {
	what string // what the entry is for, for the error when it cannot be added

	rule bool // the mark rule; the fields below it describe a route
	pref int  // the rule's priority

	v6     bool
	typ    string // "unreachable", or empty for unicast
	prefix netip.Prefix
	via    string
	dev    string
	table  string // empty is main
	metric int    // named on add and del alike; zero leaves it out of both
}

// installed lists what EnableTunRouting added, oldest first. An entry joins
// once its add succeeded and leaves once it is gone from the host, so a failed
// teardown keeps it for the next attempt.
var installed []tunEntry

// DirectBind reports how freedom outbounds leave the tunnel on this platform.
// Linux marks the sockets; EnableTunRouting installs the matching ip rule.
func DirectBind() (xraycfg.DirectBind, error) {
	return xraycfg.DirectBind{Mark: xraycfg.DirectFwMark}, nil
}

// EnableTunRouting points the system's default traffic at the TUN device and
// pins the VPN server to the physical path. It only adds: an entry already in
// place is refused before the first change, and a failure takes back exactly
// what this call added.
func EnableTunRouting(cfg TunRouteConfig) error {
	if len(cfg.ServerIPs) == 0 {
		return fmt.Errorf("не задан адрес VPN-сервера для исключения из туннеля")
	}

	// A teardown that failed left its entries owned: finish it before building
	// a second set on top.
	if err := DisableTunRouting(); err != nil {
		return err
	}

	// Resolve every server's physical path first: if this fails after the
	// split default is in place, xray's own uplink is blackholed and the
	// machine loses connectivity entirely.
	want := make([]tunEntry, 0, len(cfg.ServerIPs)+6)
	for _, ip := range cfg.ServerIPs {
		prefix, err := routeDst(ip, false)
		if err != nil {
			return err
		}
		via, dev, err := ipOps.routeGet(prefix.Addr())
		if err != nil {
			return fmt.Errorf("определить маршрут до сервера %s: %w: %w", ip, ErrNoRoute, err)
		}
		want = append(want, tunEntry{what: "исключить сервер " + ip + " из туннеля", prefix: prefix, via: via, dev: dev, metric: tunMetric})
	}

	// The physical path for marked traffic is resolved here, alongside the
	// server exceptions, for the same reason: after the split default is in
	// place the kernel answers with the tun device.
	directVia, directDev, err := ipOps.routeGet(netip.MustParseAddr(markProbe))
	if err != nil {
		return fmt.Errorf("определить физический маршрут по умолчанию: %w: %w", ErrNoRoute, err)
	}

	// Marked traffic gets its escape hatch before the split default exists,
	// otherwise direct connections loop during the gap between the two.
	want = append(want,
		tunEntry{what: "проложить прямой маршрут мимо туннеля",
			prefix: netip.PrefixFrom(netip.IPv4Unspecified(), 0), via: directVia, dev: directDev, table: directTable, metric: tunMetric},
		tunEntry{what: "вывести прямой трафик из туннеля", rule: true})

	// A11: the VPN runs without IPv6, so tun does not route it into the tunnel —
	// it refuses it, before IPv4 is captured. Unreachable rather than blackhole:
	// the app hears at once and falls back to IPv4, which the tunnel carries,
	// instead of hanging until a timeout. Link-local and LAN prefixes have
	// routes of their own, more specific than these halves, and keep working.
	// The device and metric are the kernel's own and spelled out for the
	// teardown: an IPv6 delete ignores the route type, so `del unreachable ::/1`
	// alone would remove whatever route holds that prefix.
	if cfg.Addr6 != "" && HasIPv6Stack() {
		for _, half := range blockDefault6 {
			want = append(want, tunEntry{what: "закрыть IPv6 мимо туннеля",
				v6: true, typ: "unreachable", prefix: netip.MustParsePrefix(half), dev: "lo", metric: 1024})
		}
	}

	for _, half := range splitDefault {
		want = append(want, tunEntry{what: "направить трафик в " + cfg.Iface,
			prefix: netip.MustParsePrefix(half), dev: cfg.Iface, metric: tunMetric})
	}

	// Nothing here may overwrite an entry that is already there (A08), so the
	// whole set is checked before the first change.
	pref, err := checkTunRoutingFree(cfg, want)
	if err != nil {
		return err
	}

	// add fails on an entry a client slipped in after the check instead of
	// replacing it, and an entry is owned only once its add went through. The
	// journal hears of it before the add: a run killed right after the add
	// still has the entry written down (H06).
	for _, e := range want {
		if e.rule {
			e.pref = pref
		}
		journalRoutes(append(slices.Clip(installed), e))
		if err := ipOps.add(e); err != nil {
			err = fmt.Errorf("%s: %w", e.what, err)
			if undoErr := DisableTunRouting(); undoErr != nil {
				err = fmt.Errorf("%w\nоткат не завершён: %w", err, undoErr)
			}
			return err
		}
		installed = append(installed, e)
	}

	// The preflight cannot see a client that takes one of these prefixes while
	// the adds are running, and an add no longer collides with it: ours carries
	// tunMetric, so the kernel files both routes side by side and then prefers
	// the lower metric. Our entry would sit in the table doing nothing while the
	// screen says the tunnel is up. One reread afterwards turns that into a
	// refusal — it does not close the window, it stops it passing for success.
	if err := checkTunRoutingOwned(); err != nil {
		if undoErr := DisableTunRouting(); undoErr != nil {
			err = fmt.Errorf("%w\nоткат не завершён: %w", err, undoErr)
		}
		return err
	}

	slog.Info("tun routing enabled", "iface", cfg.Iface, "excluded_servers", len(cfg.ServerIPs))
	return nil
}

// checkTunRoutingOwned refuses a set of routes that did not end up owning its
// prefixes: another client holds one of them alongside ours, or ours is already
// gone. Both mean the traffic does not go where this call just said it would.
// Rules are left to the preflight — a foreign rule for the mark is refused
// there, and ours is named by a priority no add can take over.
func checkTunRoutingOwned() error {
	routes4, err := ipOps.listRoutes(false)
	if err != nil {
		return err
	}
	var routes6 []ipRoute
	if slices.ContainsFunc(installed, func(e tunEntry) bool { return e.v6 }) {
		if routes6, err = ipOps.listRoutes(true); err != nil {
			return err
		}
	}

	var conflicts []string
	for _, e := range installed {
		if e.rule {
			continue
		}
		routes := routes4
		if e.v6 {
			routes = routes6
		}
		mine, shared, err := e.ownsPrefix(routes)
		if err != nil {
			return err
		}
		if shared || !mine {
			conflicts = append(conflicts, e.prefix.String())
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("пока ставились маршруты, эти записи занял кто-то другой: %s — чужое не перезаписываю, своё снимаю",
			strings.Join(conflicts, "; "))
	}
	return nil
}

// DisableTunRouting removes what EnableTunRouting installed, newest first, so
// the split default goes before the exceptions it relies on. Each entry is
// looked up before it is deleted: one the tun took with it is already gone, and
// one another client has since replaced is not ours to remove. An entry whose
// delete fails stays owned and the error says so; the next call retries it.
func DisableTunRouting() error {
	if len(installed) == 0 {
		return nil
	}
	routes4, err := ipOps.listRoutes(false)
	if err != nil {
		return fmt.Errorf("маршруты TUN не сняты: %w", err)
	}
	var routes6 []ipRoute
	var rules []ipRule
	if slices.ContainsFunc(installed, func(e tunEntry) bool { return e.v6 }) {
		if routes6, err = ipOps.listRoutes(true); err != nil {
			return fmt.Errorf("маршруты TUN не сняты: %w", err)
		}
	}
	if slices.ContainsFunc(installed, func(e tunEntry) bool { return e.rule }) {
		if rules, err = ipOps.listRules(); err != nil {
			return fmt.Errorf("маршруты TUN не сняты: %w", err)
		}
	}

	var kept []tunEntry
	var errs []error
	for _, e := range slices.Backward(installed) {
		routes := routes4
		if e.v6 {
			routes = routes6
		}
		present, foreign, err := e.find(routes, rules)
		if err != nil {
			errs = append(errs, err)
			kept = append(kept, e)
			continue
		}
		if foreign {
			slog.Warn("tun route replaced by another client, left in place", "route", e.String())
		}
		if !present {
			continue
		}
		if err := ipOps.del(e); err != nil {
			errs = append(errs, fmt.Errorf("снять %s: %w", e, err))
			kept = append(kept, e)
		}
	}
	slices.Reverse(kept)
	installed = kept
	journalRoutes(installed)
	if len(errs) > 0 {
		return fmt.Errorf("маршруты TUN сняты не полностью: %w", errors.Join(errs...))
	}

	slog.Info("tun routing disabled")
	return nil
}

// String names the entry the way ip would spell its add.
func (e tunEntry) String() string { return strings.Join(e.args("add"), " ") }

// args spells the entry for ip: add installs it, del removes exactly it. The
// program no longer runs ip; this is how an entry reads in a log line and an
// error, and how the tests' model of the kernel takes it.
func (e tunEntry) args(verb string) []string {
	if e.rule {
		return []string{"rule", verb, "pref", strconv.Itoa(e.pref),
			"fwmark", strconv.Itoa(xraycfg.DirectFwMark), "lookup", directTable}
	}
	var args []string
	if e.v6 {
		args = append(args, "-6")
	}
	args = append(args, "route", verb)
	if e.typ != "" {
		args = append(args, e.typ)
	}
	args = append(args, e.prefix.String())
	if e.via != "" {
		args = append(args, "via", e.via)
	}
	args = append(args, "dev", e.dev)
	if e.table != "" {
		args = append(args, "table", e.table)
	}
	if e.metric != 0 {
		args = append(args, "metric", strconv.Itoa(e.metric))
	}
	return args
}

// find looks the entry up in the host's state. present means it is still there
// as it was added, route type included: the kernel's IPv6 delete ignores the
// type, so `del unreachable ::/1` would take a prohibit route sitting where
// ours was (F03), and the only defence is not to send that delete. foreign
// means the entry is gone but another one now holds its prefix, which the
// teardown must leave alone.
func (e tunEntry) find(routes []ipRoute, rules []ipRule) (present, foreign bool, err error) {
	if e.rule {
		mark := fmt.Sprintf("%#x", xraycfg.DirectFwMark)
		for _, r := range rules {
			if r.Priority == e.pref && r.FwMark == mark && r.FwMask == "" && r.Table == directTable {
				return true, false, nil
			}
		}
		return false, false, nil
	}
	for _, r := range routes {
		if r.Table != e.table {
			continue
		}
		p, err := routeDst(r.Dst, e.v6)
		if err != nil {
			return false, false, err
		}
		if p != e.prefix {
			continue
		}
		if routeType(r.Type) == e.typ && r.Gateway == e.via && r.Dev == e.dev && r.Metric == e.metric {
			return true, false, nil
		}
		foreign = true
	}
	return false, foreign, nil
}

// ownsPrefix reports whether this entry is in the table, and whether another
// client's route holds the same prefix alongside it. find answers a teardown,
// which only needs to know which of the two it is looking at and stops at the
// first match; here both matter at once. An add naming an explicit metric no
// longer collides with a foreign route of another metric — the kernel keeps
// both and then prefers the lower one, which can be theirs.
func (e tunEntry) ownsPrefix(routes []ipRoute) (mine, shared bool, err error) {
	for _, r := range routes {
		if r.Table != e.table {
			continue
		}
		p, err := routeDst(r.Dst, e.v6)
		if err != nil {
			return false, false, err
		}
		if p != e.prefix {
			continue
		}
		if routeType(r.Type) == e.typ && r.Gateway == e.via && r.Dev == e.dev && r.Metric == e.metric {
			mine = true
			continue
		}
		shared = true
	}
	return mine, shared, nil
}

// checkTunRoutingFree refuses to route while any entry of want is already
// there, and returns the priority the mark rule is to take. A crashed run's
// leftovers match another client's byte for byte, so a match proves nothing
// about ownership and is never taken over. Only exact prefixes count: the
// default route, the LAN and other clients' more specific routes are no
// conflict. A query that fails or prints something unreadable refuses too —
// routing on a guess is how foreign entries got overwritten.
func checkTunRoutingFree(cfg TunRouteConfig, want []tunEntry) (int, error) {
	// xray assigns the address before it brings the interface up, so a tun
	// without it is not the one this core created.
	addrs, err := ipOps.linkAddrs(cfg.Iface)
	if err != nil {
		return 0, fmt.Errorf("прочитать адреса %s: %w", cfg.Iface, err)
	}
	if !slices.ContainsFunc(addrs, func(a netip.Addr) bool { return a.String() == cfg.Addr }) {
		return 0, fmt.Errorf("у %s нет адреса %s — это не интерфейс, который поднимает ядро", cfg.Iface, cfg.Addr)
	}

	// The direct table is checked whole below; every other route lands in main.
	own := map[netip.Prefix]bool{}
	for _, e := range want {
		if !e.rule && e.table == "" {
			own[e.prefix] = true
		}
	}
	families := []bool{false}
	if slices.ContainsFunc(want, func(e tunEntry) bool { return e.v6 }) {
		families = append(families, true)
	}

	var conflicts []string
	for _, v6 := range families {
		routes, err := ipOps.listRoutes(v6)
		if err != nil {
			return 0, err
		}
		for _, r := range routes {
			if !v6 && r.Table == directTable {
				conflicts = append(conflicts, "таблица "+directTable+": "+r.describe(r.Dst))
				continue
			}
			if r.Table != "" {
				continue
			}
			p, err := routeDst(r.Dst, v6)
			if err != nil {
				return 0, err
			}
			if own[p] {
				conflicts = append(conflicts, r.describe(p.String()))
			}
		}
	}

	rules, err := ipOps.listRules()
	if err != nil {
		return 0, err
	}
	for _, r := range rules {
		catches, err := r.catchesDirectMark()
		if err != nil {
			return 0, err
		}
		if catches || r.Table == directTable {
			conflicts = append(conflicts, r.describe())
		}
	}

	if len(conflicts) > 0 {
		return 0, fmt.Errorf("уже есть записи, которые ставит TUN: %s — чужое не перезаписываю, ничего не изменено (остатки упавшего запуска удалите вручную или перезагрузкой)",
			strings.Join(conflicts, "; "))
	}

	// A rule added without a priority lands just before the second rule in the
	// kernel's list: past `lookup local`, ahead of every other client's rules.
	// The mark rule takes that place by number, so its delete names it alone.
	pref := 0
	if len(rules) > 1 {
		pref = rules[1].Priority - 1
	}
	if pref < 1 {
		return 0, fmt.Errorf("нет свободного приоритета для правила fwmark %d перед правилами других программ", xraycfg.DirectFwMark)
	}
	return pref, nil
}

// ipRoute is one route, reduced to what the routing code reads and spelled the
// way `ip -j -N route show` spells it: no table for main, a number for any
// other; "default" or a prefix for the destination, a host route without its
// length; the type as the kernel's number, absent for unicast.
type ipRoute struct {
	Dst     string `json:"dst"`
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	Table   string `json:"table"`
	Metric  int    `json:"metric"`
	// Type is absent for a plain unicast route. A document that puts something
	// other than a string here fails to decode, which stays a read error: a
	// type nobody could read must never pass for unicast.
	Type string `json:"type"`
}

// routeType normalizes the route type as ip prints it. Queries carry -N, so the
// kernel's numbers come through as strings ("6" blackhole, "7" unreachable,
// "8" prohibit) and a unicast route carries no type at all; without -N the same
// types print by name. Only the two kinds this code installs are named. Any
// other valid type is left as it came and so compares unequal to both — it is
// somebody else's entry, which is the answer ownership needs.
func routeType(s string) string {
	switch s {
	case "", "1", "unicast":
		return ""
	case "7", "unreachable":
		return "unreachable"
	}
	return s
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

// routeDst reads a destination the way ip prints it: "default" for the whole
// family, a host route without its /32 or /128.
func routeDst(dst string, v6 bool) (netip.Prefix, error) {
	if dst == "default" {
		if v6 {
			return netip.PrefixFrom(netip.IPv6Unspecified(), 0), nil
		}
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0), nil
	}
	if addr, err := netip.ParseAddr(dst); err == nil {
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	p, err := netip.ParsePrefix(dst)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("разобрать назначение маршрута %q: %w", dst, err)
	}
	return p, nil
}

// ipRule is one policy rule, spelled as `ip -j -N rule show` spells it: fwmark
// and fwmask in hex, the mask left out when it is all ones.
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
