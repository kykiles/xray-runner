//go:build windows

package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeIP models powershell, route.exe and netsh over an in-memory routing
// table, so the routing logic is verified without admin rights or touching the
// host. The read-only scripts answer from that table with the properties they
// select, the way ConvertTo-Json prints them: Get-NetRoute lists the table,
// Find-NetRoute picks the best route (longest prefix, then lowest metric) and
// prints it as a lone object, which is what ConvertTo-Json makes of a single
// pipeline item. A route is keyed by prefix, interface and next hop, as
// Windows keys it: an add of a key already there fails, and Remove-NetRoute
// removes the one route matching all three. A script the fake does not know
// fails, so the code under test cannot lean on output nobody modelled.
type fakeIP struct {
	routes   []fakeRoute
	tunIndex int // ifIndex of the xray-tun adapter; zero means there is none
	// selfOnLink records a route whose gateway is the tun's own address as
	// on-link, which Windows may do with an interface's own address.
	selfOnLink bool

	bindAlias  string
	bindErr    error
	bindScript string
	findScript string

	raw      map[string]string // replaces a script's output; keyed "find", "adapter", "routes"
	queryErr map[string]error  // fails a script; same keys

	// before runs ahead of every mutation — a client racing the preflight;
	// fail makes a mutation fail when it returns true.
	before func(cmd string)
	fail   func(cmd string) bool

	scripts []string // every script run
	cmds    []string // every mutation tried, failed ones included
}

type fakeRoute struct {
	dst     netip.Prefix
	nextHop string // "0.0.0.0" or "::" on-link, as Windows prints it
	ifIndex int
	metric  int
}

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// winHost is a machine right after xray brought the wintun adapter up and
// before any routing: default routes and the LAN on interface 12, the tun's
// own prefixes on 27.
func winHost() *fakeIP {
	return &fakeIP{
		tunIndex: 27,
		routes: []fakeRoute{
			{dst: pfx("0.0.0.0/0"), nextHop: "192.168.31.1", ifIndex: 12},
			{dst: pfx("192.168.31.0/24"), nextHop: "0.0.0.0", ifIndex: 12, metric: 256},
			{dst: pfx("10.0.0.0/24"), nextHop: "0.0.0.0", ifIndex: 27, metric: 256},
			{dst: pfx("::/0"), nextHop: "fe80::1", ifIndex: 12},
			{dst: pfx("fe80::/64"), nextHop: "::", ifIndex: 12, metric: 256},
			{dst: pfx("fdfe:dcba:9876::/126"), nextHop: "::", ifIndex: 27, metric: 256},
		},
	}
}

func (f *fakeIP) lookPath(bin string) error { return nil }

func (f *fakeIP) run(bin string, args ...string) ([]byte, error) {
	if bin == "powershell" {
		return f.script(args[len(args)-1])
	}
	return f.mutation(bin+" "+strings.Join(args, " "), func() ([]byte, error) { return f.mutate(bin, args) })
}

// mutation records cmd and applies it unless the test makes it fail.
func (f *fakeIP) mutation(cmd string, apply func() ([]byte, error)) ([]byte, error) {
	f.cmds = append(f.cmds, cmd)
	if f.before != nil {
		f.before(cmd)
	}
	if f.fail != nil && f.fail(cmd) {
		return []byte("Access is denied."), errors.New("exit status 1")
	}
	return apply()
}

var findTarget = regexp.MustCompile(`Find-NetRoute -RemoteIPAddress '([^']*)'`)

func (f *fakeIP) script(s string) ([]byte, error) {
	f.scripts = append(f.scripts, s)
	// Checked first: the DirectBind script names both cmdlets.
	if strings.Contains(s, "InterfaceAlias") {
		f.bindScript = s
		return []byte(f.bindAlias), f.bindErr
	}
	if strings.Contains(s, "Remove-NetRoute") {
		return f.removeRoutes(s)
	}
	if !strings.Contains(s, "| ConvertTo-Json") {
		return nil, fmt.Errorf("fakeIP: script without structured output %q", s)
	}

	var key string
	var out any
	switch {
	case strings.Contains(s, "Find-NetRoute"):
		f.findScript = s
		key = "find"
		m := findTarget.FindStringSubmatch(s)
		if m == nil {
			return nil, fmt.Errorf("fakeIP: unmodelled script %q", s)
		}
		addr, err := netip.ParseAddr(m[1])
		if err != nil {
			return nil, fmt.Errorf("fakeIP: Find-NetRoute target %q: %w", m[1], err)
		}
		r, ok := f.best(addr)
		if !ok {
			return []byte("Find-NetRoute : No matching MSFT_NetRoute objects found"), errors.New("exit status 1")
		}
		out = map[string]any{"NextHop": r.nextHop, "InterfaceIndex": r.ifIndex}
	case strings.Contains(s, "Get-NetAdapter -Name 'xray-tun'"):
		key = "adapter"
		if f.tunIndex == 0 {
			return []byte("Get-NetAdapter : No MSFT_NetAdapter objects found"), errors.New("exit status 1")
		}
		out = map[string]any{"ifIndex": f.tunIndex}
	case strings.Contains(s, "Get-NetRoute -PolicyStore ActiveStore"):
		key, out = "routes", f.routeJSON()
	default:
		return nil, fmt.Errorf("fakeIP: unmodelled script %q", s)
	}

	if err := f.queryErr[key]; err != nil {
		return []byte("script failed"), err
	}
	if raw, ok := f.raw[key]; ok {
		return []byte(raw), nil
	}
	return json.Marshal(out)
}

var removeRoute = regexp.MustCompile(`Remove-NetRoute -DestinationPrefix '([^']*)' -InterfaceIndex (\d+) -NextHop '([^']*)' -PolicyStore ActiveStore -Confirm:\$false`)

// removeRoutes runs a batch of Remove-NetRoute calls in order. Each removes the
// one route matching its prefix, interface and next hop, and one that matches
// nothing is an error. The batch must stop at the first error, or it would
// report the removals after it as done when they may not be.
func (f *fakeIP) removeRoutes(s string) ([]byte, error) {
	stop := strings.Index(s, "$ErrorActionPreference = 'Stop'; ")
	if stop < 0 || stop > strings.Index(s, "Remove-NetRoute") {
		return nil, fmt.Errorf("fakeIP: a removal batch that goes on past an error %q", s)
	}
	calls := removeRoute.FindAllStringSubmatch(s, -1)
	if len(calls) != strings.Count(s, "Remove-NetRoute") {
		return nil, fmt.Errorf("fakeIP: unmodelled Remove-NetRoute in %q", s)
	}
	for _, c := range calls {
		dst, err := netip.ParsePrefix(c[1])
		if err != nil {
			return nil, fmt.Errorf("fakeIP: prefix %q: %w", c[1], err)
		}
		index, _ := strconv.Atoi(c[2])
		hop := c[3]
		out, err := f.mutation("Remove-NetRoute "+c[1]+" if "+c[2]+" via "+hop, func() ([]byte, error) {
			i := slices.IndexFunc(f.routes, func(r fakeRoute) bool {
				return r.dst == dst && r.ifIndex == index && r.nextHop == hop
			})
			if i < 0 {
				return []byte("Remove-NetRoute : No matching MSFT_NetRoute objects found"), errors.New("exit status 1")
			}
			f.routes = slices.Delete(f.routes, i, i+1)
			return nil, nil
		})
		if err != nil {
			return out, err
		}
	}
	return nil, nil
}

// best picks the route Windows would use: the longest matching prefix, then
// the lowest metric.
func (f *fakeIP) best(addr netip.Addr) (fakeRoute, bool) {
	var best fakeRoute
	found := false
	for _, r := range f.routes {
		if !r.dst.Contains(addr) {
			continue
		}
		if !found || r.dst.Bits() > best.dst.Bits() || r.dst.Bits() == best.dst.Bits() && r.metric < best.metric {
			best, found = r, true
		}
	}
	return best, found
}

func (f *fakeIP) routeJSON() []map[string]any {
	out := []map[string]any{}
	for _, r := range f.routes {
		out = append(out, map[string]any{
			"DestinationPrefix": r.dst.String(), "NextHop": r.nextHop, "InterfaceIndex": r.ifIndex, "RouteMetric": r.metric,
		})
	}
	return out
}

// state prints the routing table, for before/after comparisons.
func (f *fakeIP) state() string {
	b, _ := json.Marshal(f.routeJSON())
	return string(b)
}

// recreateTun is xray dying and coming back: the old adapter takes its routes
// with it, and the new one gets another index.
func (f *fakeIP) recreateTun(index int) {
	f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r.ifIndex == f.tunIndex })
	f.tunIndex = index
	f.routes = append(f.routes,
		fakeRoute{dst: pfx("10.0.0.0/24"), nextHop: "0.0.0.0", ifIndex: index, metric: 256},
		fakeRoute{dst: pfx("fdfe:dcba:9876::/126"), nextHop: "::", ifIndex: index, metric: 256})
}

// mutate applies route.exe and netsh. The code under test no longer deletes
// through them, but the deletes stay modelled with their real reach: a delete
// by destination creeping back would remove the foreign routes the tests watch
// instead of failing unseen, since route.exe errors used to be ignored.
func (f *fakeIP) mutate(bin string, args []string) ([]byte, error) {
	switch {
	// route add DEST mask MASK GATEWAY if INDEX
	case bin == "route" && len(args) == 7 && args[0] == "add" && args[2] == "mask" && args[5] == "if":
		dst, err := maskPrefix(args[1], args[3])
		if err != nil {
			return nil, err
		}
		index, err := strconv.Atoi(args[6])
		if err != nil {
			return nil, fmt.Errorf("fakeIP: interface %q: %w", args[6], err)
		}
		hop := args[4]
		if f.selfOnLink && hop == "10.0.0.1" {
			hop = "0.0.0.0"
		}
		return f.add(fakeRoute{dst: dst, nextHop: hop, ifIndex: index})
	// route delete DEST mask MASK: without a gateway it removes every route
	// to the prefix, another client's included.
	case bin == "route" && len(args) == 4 && args[0] == "delete" && args[2] == "mask":
		dst, err := maskPrefix(args[1], args[3])
		if err != nil {
			return nil, err
		}
		f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r.dst == dst })
		return nil, nil
	// netsh interface ipv6 add|delete route prefix=P interface=I store=active [nexthop=H]
	case bin == "netsh" && len(args) >= 6 && args[0] == "interface" && args[1] == "ipv6" && args[3] == "route":
		kv := map[string]string{}
		for _, a := range args[4:] {
			k, v, _ := strings.Cut(a, "=")
			kv[k] = v
		}
		dst, err := netip.ParsePrefix(kv["prefix"])
		if err != nil {
			return nil, fmt.Errorf("fakeIP: prefix %q: %w", kv["prefix"], err)
		}
		index, err := strconv.Atoi(kv["interface"])
		if err != nil {
			return nil, fmt.Errorf("fakeIP: interface %q: %w", kv["interface"], err)
		}
		switch args[2] {
		case "add":
			hop := kv["nexthop"]
			if hop == "" || f.selfOnLink && hop == "fdfe:dcba:9876::1" {
				hop = "::"
			}
			return f.add(fakeRoute{dst: dst, nextHop: hop, ifIndex: index})
		case "delete":
			f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r.dst == dst && r.ifIndex == index })
			return nil, nil
		}
	}
	return nil, fmt.Errorf("fakeIP: unmodelled command %s %q", bin, args)
}

// add refuses a route that is already there — same prefix, interface and next
// hop — the way route.exe and netsh answer "The object already exists".
func (f *fakeIP) add(r fakeRoute) ([]byte, error) {
	for _, e := range f.routes {
		if e.dst == r.dst && e.ifIndex == r.ifIndex && e.nextHop == r.nextHop {
			return []byte("The route addition failed: The object already exists."), errors.New("exit status 1")
		}
	}
	f.routes = append(f.routes, r)
	return nil, nil
}

func maskPrefix(dest, mask string) (netip.Prefix, error) {
	addr, err := netip.ParseAddr(dest)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("fakeIP: destination %q: %w", dest, err)
	}
	m, err := netip.ParseAddr(mask)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("fakeIP: mask %q: %w", mask, err)
	}
	bits, _ := net.IPMask(m.AsSlice()).Size()
	return netip.PrefixFrom(addr, bits), nil
}

func (f *fakeIP) has(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func (f *fakeIP) hasRoute(r fakeRoute) bool { return slices.Contains(f.routes, r) }

// holds reports whether any route has the prefix.
func (f *fakeIP) holds(prefix string) bool {
	return slices.ContainsFunc(f.routes, func(r fakeRoute) bool { return r.dst == pfx(prefix) })
}

// removals lists the Remove-NetRoute calls tried, in order.
func (f *fakeIP) removals() []string {
	var out []string
	for _, c := range f.cmds {
		if strings.HasPrefix(c, "Remove-NetRoute") {
			out = append(out, c)
		}
	}
	return out
}

func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	orig := ipCmd
	ipCmd = f
	t.Cleanup(func() {
		ipCmd = orig
		installed = nil
	})
}

var tunCfg = TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"}}

// Without these two routes the wintun adapter exists but no traffic ever
// enters it — the bug this whole file addresses.
func TestEnableTunRouting_SendsDefaultTrafficIntoTun(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 0.0.0.0 mask 128.0.0.0 10.0.0.1 if 27") {
		t.Errorf("missing lower split-default route, got: %v", f.cmds)
	}
	if !f.has("route add 128.0.0.0 mask 128.0.0.0 10.0.0.1 if 27") {
		t.Errorf("missing upper split-default route, got: %v", f.cmds)
	}
}

// The tunnel's own packets to the VPS must keep using the physical adapter,
// otherwise xray's uplink routes into the tun it is serving and deadlocks.
func TestEnableTunRouting_ExcludesServerViaPhysicalGateway(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 45.150.32.235 mask 255.255.255.255 192.168.31.1 if 12") {
		t.Errorf("missing server exclusion route, got: %v", f.cmds)
	}
}

// Installing the split default before knowing the server's physical path would
// blackhole the tunnel's own uplink, so this must fail before touching routes.
func TestEnableTunRouting_FailsWithoutTouchingRoutesWhenServerPathUnknown(t *testing.T) {
	f := winHost()
	f.queryErr = map[string]error{"find": errors.New("no route")}
	withFakeIP(t, f)

	// Reported as ErrNoRoute: offline for now, which a restarted core waits out
	// instead of ending the session (G06).
	if err := EnableTunRouting(tunCfg); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("err = %v, want ErrNoRoute when the server's physical route can't be resolved", err)
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

// Find-NetRoute also returns the source MSFT_NetIPAddress, which has an
// InterfaceIndex but no NextHop. Picking it yields a route command with an
// empty gateway, so the pipeline must keep only the route object.
func TestEnableTunRouting_IgnoresAddressObjectFromFindNetRoute(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if !strings.Contains(f.findScript, "Where-Object NextHop") {
		t.Errorf("Find-NetRoute output must be filtered to route objects, got: %q", f.findScript)
	}
}

// A crash never runs the teardown, so the next start meets its own leftovers —
// and they look exactly like another client's routes. Nothing proves they are
// ours, so Enable names them and refuses instead of taking them over.
func TestEnableTunRouting_RefusesLeftoversFromACrashedRun(t *testing.T) {
	crash := func(t *testing.T) *fakeIP {
		f := winHost()
		withFakeIP(t, f)
		if err := EnableTunRouting(tunCfg6); err != nil {
			t.Fatalf("EnableTunRouting: %v", err)
		}
		// The process died, and what it knew about its routes died with it.
		installed = nil
		f.cmds = nil
		return f
	}
	refuse := func(t *testing.T, f *fakeIP, leftovers []string) {
		t.Helper()
		before := f.state()
		err := EnableTunRouting(tunCfg6)
		if err == nil {
			t.Fatalf("took over a crashed run's leftovers: %v", f.cmds)
		}
		for _, dst := range leftovers {
			if !strings.Contains(err.Error(), dst) {
				t.Errorf("error does not name the leftover %s: %v", dst, err)
			}
		}
		if len(f.cmds) != 0 {
			t.Errorf("routes changed on refusal: %v", f.cmds)
		}
		if f.state() != before {
			t.Errorf("routing table changed on refusal:\nbefore %s\nafter  %s", before, f.state())
		}
	}

	t.Run("adapter still up", func(t *testing.T) {
		f := crash(t)
		refuse(t, f, []string{"45.150.32.235/32", "0.0.0.0/1", "128.0.0.0/1", "2a00:1450::64/128", "::/1", "8000::/1"})
	})
	// The routes through the dead adapter went with it; the server exceptions
	// on the physical one stay behind.
	t.Run("adapter recreated", func(t *testing.T) {
		f := crash(t)
		f.recreateTun(31)
		refuse(t, f, []string{"45.150.32.235/32", "2a00:1450::64/128"})
	})
}

// Any route holding a prefix Enable is about to create belongs to someone —
// another VPN, an admin, a crashed run — whatever its interface, next hop or
// metric, and overwriting it is how foreign routes got destroyed (A08).
func TestEnableTunRouting_RefusesRoutesItWouldCreate(t *testing.T) {
	cases := []struct {
		name  string
		cfg   TunRouteConfig
		route fakeRoute
	}{
		{"server via another gateway", tunCfg, fakeRoute{dst: pfx("45.150.32.235/32"), nextHop: "192.168.31.254", ifIndex: 12}},
		{"server on another interface", tunCfg, fakeRoute{dst: pfx("45.150.32.235/32"), nextHop: "0.0.0.0", ifIndex: 40, metric: 5}},
		{"lower half of another VPN", tunCfg, fakeRoute{dst: pfx("0.0.0.0/1"), nextHop: "10.8.0.1", ifIndex: 40}},
		{"upper half of another VPN", tunCfg, fakeRoute{dst: pfx("128.0.0.0/1"), nextHop: "10.8.0.1", ifIndex: 40}},
		{"IPv6 server", tunCfg6, fakeRoute{dst: pfx("2a00:1450::64/128"), nextHop: "fe80::2", ifIndex: 40}},
		{"IPv6 lower half", tunCfg6, fakeRoute{dst: pfx("::/1"), nextHop: "::", ifIndex: 40}},
		{"IPv6 upper half", tunCfg6, fakeRoute{dst: pfx("8000::/1"), nextHop: "::", ifIndex: 40}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := winHost()
			f.routes = append(f.routes, c.route)
			withFakeIP(t, f)
			before := f.state()

			err := EnableTunRouting(c.cfg)
			if err == nil {
				t.Fatalf("routed over an existing %s: %v", c.route.dst, f.cmds)
			}
			if !strings.Contains(err.Error(), c.route.dst.String()) {
				t.Errorf("error does not name %s: %v", c.route.dst, err)
			}
			if len(f.cmds) != 0 {
				t.Errorf("routes changed on refusal: %v", f.cmds)
			}
			if f.state() != before {
				t.Errorf("routing table changed on refusal:\nbefore %s\nafter  %s", before, f.state())
			}
		})
	}
}

// Routing on a guess is how foreign routes got overwritten: a lookup that
// fails or answers something unreadable stops Enable before the first change.
func TestEnableTunRouting_RefusesWhenStateIsUnknown(t *testing.T) {
	raw := func(key, out string) func(*fakeIP) {
		return func(f *fakeIP) { f.raw = map[string]string{key: out} }
	}
	cases := []struct {
		name  string
		cfg   TunRouteConfig
		setup func(*fakeIP)
	}{
		{"route table unreadable", tunCfg, func(f *fakeIP) { f.queryErr = map[string]error{"routes": errors.New("exit status 1")} }},
		{"route table not JSON", tunCfg, raw("routes", "Get-NetRoute : Access is denied.")},
		{"route table empty", tunCfg, raw("routes", "")},
		{"destination without a length", tunCfg, raw("routes", `[{"DestinationPrefix":"45.150.32.235","NextHop":"0.0.0.0","InterfaceIndex":12}]`)},
		{"server path as text", tunCfg, raw("find", "192.168.31.1 12\r\n")},
		{"no server path", tunCfg, raw("find", "")},
		{"server path of the other family", tunCfg, raw("find", `{"NextHop":"fe80::1","InterfaceIndex":12}`)},
		{"server path without an interface", tunCfg, raw("find", `{"NextHop":"192.168.31.1","InterfaceIndex":0}`)},
		{"two tun adapters", tunCfg, raw("adapter", `[{"ifIndex":27},{"ifIndex":31}]`)},
		{"no IPv6 path to the server", tunCfg6, func(f *fakeIP) {
			f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r.dst == pfx("::/0") })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := winHost()
			c.setup(f)
			withFakeIP(t, f)

			if err := EnableTunRouting(c.cfg); err == nil {
				t.Fatalf("routed without knowing the host's state: %v", f.cmds)
			}
			if len(f.cmds) != 0 {
				t.Errorf("routes changed before the host's state was known: %v", f.cmds)
			}
		})
	}
}

// The server addresses are spliced into PowerShell scripts, so anything but a
// plain address of the right family is refused before a script runs.
func TestEnableTunRouting_RefusesAddressesThatAreNotIPs(t *testing.T) {
	cases := []struct {
		name string
		cfg  TunRouteConfig
	}{
		{"script in the address", TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1",
			ServerIPs: []string{"45.150.32.235'; Remove-Item C:\\Users -Recurse; '"}}},
		{"IPv6 among IPv4", TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1",
			ServerIPs: []string{"2a00:1450::64"}}},
		{"IPv4 among IPv6", TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"},
			Addr6: "fdfe:dcba:9876::1", ServerIPs6: []string{"45.150.32.235"}}},
		{"script in the zone", TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"},
			Addr6: "fdfe:dcba:9876::1", ServerIPs6: []string{"fe80::1%'; Remove-Item C:\\Users -Recurse; '"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := winHost()
			withFakeIP(t, f)

			if err := EnableTunRouting(c.cfg); err == nil {
				t.Fatalf("routed a server that is not an address: %v", f.cmds)
			}
			if len(f.scripts) != 0 {
				t.Errorf("ran a script before the addresses were checked: %q", f.scripts)
			}
			if len(f.cmds) != 0 {
				t.Errorf("routes changed: %v", f.cmds)
			}
		})
	}
}

// Only the exact prefixes Enable creates count: the default routes, the LAN,
// other clients' more specific routes and, in tun mode, IPv6 are left alone.
func TestEnableTunRouting_LeavesUnrelatedRoutesAlone(t *testing.T) {
	f := winHost()
	foreign := []fakeRoute{
		{dst: pfx("45.150.32.0/24"), nextHop: "192.168.31.254", ifIndex: 12},
		{dst: pfx("1.1.1.1/32"), nextHop: "10.8.0.1", ifIndex: 40},
		{dst: pfx("::/1"), nextHop: "::", ifIndex: 40},
	}
	f.routes = append(f.routes, foreign...)
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	for _, r := range foreign {
		if !f.hasRoute(r) {
			t.Errorf("unrelated route %s via %s if %d is gone", r.dst, r.nextHop, r.ifIndex)
		}
	}
}

// The preflight cannot stop a client that adds a route after it ran. Enable
// only adds, so the client's route survives either way: the same route makes
// the add fail, and the same prefix elsewhere simply lives beside ours.
func TestEnableTunRouting_FailsWhenAClientRacesThePreflight(t *testing.T) {
	cases := []struct {
		name   string
		racer  fakeRoute
		failed bool
	}{
		{"the same route", fakeRoute{dst: pfx("0.0.0.0/1"), nextHop: "10.0.0.1", ifIndex: 27}, true},
		{"the same prefix elsewhere", fakeRoute{dst: pfx("0.0.0.0/1"), nextHop: "10.8.0.1", ifIndex: 40}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := winHost()
			withFakeIP(t, f)
			original := f.state()
			f.before = func(string) {
				f.routes = append(f.routes, c.racer)
				f.before = nil
			}

			err := EnableTunRouting(tunCfg)
			if c.failed && err == nil {
				t.Fatalf("installed over a route added after the check: %v", f.cmds)
			}
			if !c.failed {
				if err != nil {
					t.Fatalf("EnableTunRouting: %v", err)
				}
				if err := DisableTunRouting(); err != nil {
					t.Fatalf("DisableTunRouting: %v", err)
				}
			}
			if !f.hasRoute(c.racer) {
				t.Fatalf("the racing client's route is gone: %v", f.cmds)
			}
			f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r == c.racer })
			if f.state() != original {
				t.Errorf("routes of ours left behind:\nbefore %s\nafter  %s", original, f.state())
			}
		})
	}
}

// A failed add takes back exactly the routes this call added, newest first, and
// nothing else.
func TestEnableTunRouting_RollsBackEveryFailure(t *testing.T) {
	// The six adds: IPv4 server, both IPv4 halves, IPv6 server, both IPv6 halves.
	for n := 1; n <= 6; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			f := winHost()
			withFakeIP(t, f)
			original := f.state()
			adds := 0
			f.fail = func(cmd string) bool {
				if strings.Contains(cmd, " add ") {
					adds++
					return adds == n
				}
				return false
			}

			if err := EnableTunRouting(tunCfg6); err == nil {
				t.Fatal("Enable succeeded past a failed add")
			}
			if f.state() != original {
				t.Errorf("rollback left the table changed:\nbefore %s\nafter  %s", original, f.state())
			}
			if got := f.removals(); len(got) != n-1 {
				t.Errorf("rollback removed %d routes, want exactly the %d added: %v", len(got), n-1, f.cmds)
			}
		})
	}
}

func TestEnableTunRouting_FailsWhenTunAdapterMissing(t *testing.T) {
	f := winHost()
	f.tunIndex = 0
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the tun adapter has no interface index")
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

// Each route is removed by its prefix, interface and next hop, newest first,
// and the table ends up as Enable found it.
func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	original := f.state()

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}

	want := []string{
		"Remove-NetRoute 128.0.0.0/1 if 27 via 10.0.0.1",
		"Remove-NetRoute 0.0.0.0/1 if 27 via 10.0.0.1",
		"Remove-NetRoute 45.150.32.235/32 if 12 via 192.168.31.1",
	}
	if got := f.removals(); !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v", got, want)
	}
	if f.state() != original {
		t.Errorf("table not restored:\nbefore %s\nafter  %s", original, f.state())
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not touch or even read the host's table.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}

	if len(f.cmds) != 0 || len(f.scripts) != 0 {
		t.Errorf("expected no commands, got: %v %q", f.cmds, f.scripts)
	}
}

func TestDisableTunRouting_IsNotRepeated(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	f.cmds, f.scripts = nil, nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("second DisableTunRouting: %v", err)
	}
	if len(f.cmds) != 0 || len(f.scripts) != 0 {
		t.Errorf("second teardown ran again: %v %q", f.cmds, f.scripts)
	}
}

// When xray dies its adapter takes the tun's routes with it. Those are already
// gone and cost nothing; the server exceptions on the physical adapter remain
// and are removed.
func TestDisableTunRouting_AfterTheTunIsGone(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.recreateTun(31)
	f.cmds = nil
	fresh := winHost()
	fresh.recreateTun(31)

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	want := []string{
		"Remove-NetRoute 2a00:1450::64/128 if 12 via fe80::1",
		"Remove-NetRoute 45.150.32.235/32 if 12 via 192.168.31.1",
	}
	if got := f.removals(); !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v", got, want)
	}
	if f.state() != fresh.state() {
		t.Errorf("table not restored:\nwant %s\ngot  %s", fresh.state(), f.state())
	}
}

// A prefix of ours that another client has since taken over holds their route,
// not ours: it stays where it is, and the rest of ours is removed.
func TestDisableTunRouting_LeavesAReplacedEntryAlone(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	theirs := map[netip.Prefix]fakeRoute{
		pfx("45.150.32.235/32"): {dst: pfx("45.150.32.235/32"), nextHop: "10.8.0.1", ifIndex: 40},
		pfx("::/1"):             {dst: pfx("::/1"), nextHop: "::", ifIndex: 40},
	}
	for i, r := range f.routes {
		if other, ok := theirs[r.dst]; ok {
			f.routes[i] = other
		}
	}

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, r := range theirs {
		if !f.hasRoute(r) {
			t.Errorf("another client's %s was removed: %v", r.dst, f.cmds)
		}
	}
	for _, p := range []string{"0.0.0.0/1", "128.0.0.0/1", "2a00:1450::64/128", "8000::/1"} {
		if f.holds(p) {
			t.Errorf("our %s is still there: %s", p, f.state())
		}
	}
}

// A failed removal leaves a route of ours in place: the teardown must say so
// and try again next time — from Disable, or from the next Enable before it
// builds anything on top.
func TestDisableTunRouting_KeepsWhatItFailedToRemove(t *testing.T) {
	isRemoval := func(cmd string) bool {
		return strings.HasPrefix(cmd, "Remove-NetRoute") || strings.Contains(cmd, " delete ")
	}
	failSecondRemoval := func(f *fakeIP) {
		removals := 0
		f.fail = func(cmd string) bool {
			if isRemoval(cmd) {
				removals++
				return removals == 2
			}
			return false
		}
	}
	setup := func(t *testing.T) (*fakeIP, string) {
		f := winHost()
		withFakeIP(t, f)
		original := f.state()
		if err := EnableTunRouting(tunCfg6); err != nil {
			t.Fatalf("EnableTunRouting: %v", err)
		}
		failSecondRemoval(f)
		if err := DisableTunRouting(); err == nil {
			t.Fatalf("reported a clean teardown with routes left: %s", f.state())
		}
		f.fail = nil
		return f, original
	}

	t.Run("retried by Disable", func(t *testing.T) {
		f, original := setup(t)
		if err := DisableTunRouting(); err != nil {
			t.Fatalf("retried DisableTunRouting: %v", err)
		}
		if f.state() != original {
			t.Errorf("the retry did not finish the teardown:\nbefore %s\nafter  %s", original, f.state())
		}
	})
	t.Run("finished by the next Enable", func(t *testing.T) {
		f, original := setup(t)
		if err := EnableTunRouting(tunCfg6); err != nil {
			t.Fatalf("EnableTunRouting after a failed teardown: %v", err)
		}
		if err := DisableTunRouting(); err != nil {
			t.Fatalf("DisableTunRouting: %v", err)
		}
		if f.state() != original {
			t.Errorf("table not restored:\nbefore %s\nafter  %s", original, f.state())
		}
	})
}

// Without the table there is no telling ours from a foreign route, so nothing
// is removed and the routes stay owned for the next attempt.
func TestDisableTunRouting_KeepsEverythingWhenStateIsUnknown(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	original := f.state()
	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.queryErr = map[string]error{"routes": errors.New("exit status 1")}
	f.cmds = nil

	if err := DisableTunRouting(); err == nil {
		t.Fatal("reported a clean teardown without reading the table")
	}
	if len(f.cmds) != 0 {
		t.Errorf("removed routes without reading the table: %v", f.cmds)
	}
	f.queryErr = nil
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("retried DisableTunRouting: %v", err)
	}
	if f.state() != original {
		t.Errorf("table not restored:\nbefore %s\nafter  %s", original, f.state())
	}
}

// route.exe may keep a gateway that is the interface's own address as an
// on-link route. The teardown must still know that route as ours.
func TestDisableTunRouting_RecognizesItsOwnAddressKeptAsOnLink(t *testing.T) {
	f := winHost()
	f.selfOnLink = true
	withFakeIP(t, f)
	original := f.state()

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	if f.state() != original {
		t.Errorf("table not restored:\nbefore %s\nafter  %s", original, f.state())
	}
}

// After a full teardown the next Enable starts over: xray came back with a new
// adapter index, and nothing from the old generation may leak into the new one.
func TestEnableTunRouting_ReinstallsOnANewAdapter(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.recreateTun(31)
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	fresh := f.state()
	f.cmds = nil

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting on the new adapter: %v", err)
	}
	for _, want := range []string{
		"route add 0.0.0.0 mask 128.0.0.0 10.0.0.1 if 31",
		"netsh interface ipv6 add route prefix=::/1 interface=31 store=active nexthop=fdfe:dcba:9876::1",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "if 27") || strings.Contains(c, "interface=27") {
			t.Errorf("the old adapter's index reached the new generation: %q", c)
		}
	}
	if f.state() != fresh {
		t.Errorf("table not restored:\nbefore %s\nafter  %s", fresh, f.state())
	}
}

// Windows ignores sockopt.mark, so freedom outbounds are pinned to the physical
// adapter by name. Xray looks that name up with net.InterfaceByName, which on
// Windows matches the adapter's InterfaceAlias.
func TestDirectBind_ReportsPhysicalAdapter(t *testing.T) {
	f := &fakeIP{bindAlias: "Ethernet\r\n"}
	withFakeIP(t, f)

	bind, err := DirectBind()
	if err != nil {
		t.Fatalf("DirectBind: %v", err)
	}

	if bind.Interface != "Ethernet" {
		t.Errorf("Interface = %q, want Ethernet", bind.Interface)
	}
	if bind.Mark != 0 {
		t.Errorf("Mark = %d, want 0: xray does not implement it on windows", bind.Mark)
	}
}

// Binding to an empty adapter name would silently disable the escape hatch and
// bring the routing loop back, so it has to fail loudly instead.
func TestDirectBind_FailsWhenAdapterIsUnknown(t *testing.T) {
	f := &fakeIP{bindAlias: "  \r\n"}
	withFakeIP(t, f)

	if _, err := DirectBind(); err == nil {
		t.Fatal("expected an error when the physical adapter cannot be named")
	}
}

// A localized alias read in the console codepage reaches xray as mojibake,
// net.InterfaceByName misses, and the direct traffic loops back into the tun —
// the failure seen on the 2026-08-12 diagnostic.
func TestDirectBind_FailsWhenAliasIsMojibake(t *testing.T) {
	// "Беспроводная сеть" as cp866 bytes.
	f := &fakeIP{bindAlias: "\x81\xa5\xe1\xaf\xe0\xae\xa2\xae\xa4\xad\xa0\xef \xe1\xa5\xe2\xec\r\n"}
	withFakeIP(t, f)

	if _, err := DirectBind(); err == nil {
		t.Fatal("expected an error when the adapter alias is not valid UTF-8")
	}
}

func TestDirectBind_RequestsUTF8Output(t *testing.T) {
	f := &fakeIP{bindAlias: "Ethernet\r\n"}
	withFakeIP(t, f)

	if _, err := DirectBind(); err != nil {
		t.Fatalf("DirectBind: %v", err)
	}
	if !strings.Contains(f.bindScript, "OutputEncoding") {
		t.Errorf("script must set the output encoding, got: %s", f.bindScript)
	}
}

func TestDirectBind_FailsWhenLookupFails(t *testing.T) {
	f := &fakeIP{bindErr: errors.New("Find-NetRoute failed")}
	withFakeIP(t, f)

	if _, err := DirectBind(); err == nil {
		t.Fatal("expected an error when the route lookup fails")
	}
}

// Split mode claims IPv6 as well: without these routes a v6-capable app prefers
// the AAAA record and leaves the machine with its real address while the status
// screen reports a healthy tunnel.
var tunCfg6 = TunRouteConfig{
	Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"},
	Addr6: "fdfe:dcba:9876::1", ServerIPs6: []string{"2a00:1450::64"},
}

func TestEnableTunRouting_SendsIPv6IntoTunAndExcludesServer(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	// The server's exception goes on the physical interface, the two default
	// halves on the tun — and the exception first, so the uplink is never
	// blackholed in between.
	if !f.has("netsh interface ipv6 add route prefix=2a00:1450::64/128 interface=12 store=active nexthop=fe80::1") {
		t.Errorf("missing IPv6 server exclusion, got: %v", f.cmds)
	}
	for _, half := range []string{"::/1", "8000::/1"} {
		want := "netsh interface ipv6 add route prefix=" + half + " interface=27 store=active nexthop=fdfe:dcba:9876::1"
		if !f.has(want) {
			t.Errorf("missing IPv6 default half %q, got: %v", half, f.cmds)
		}
	}
}

// Tun mode leaves IPv6 alone, so an empty Addr6 must not produce a single netsh
// call: the mode's behaviour is unchanged by the split work.
func TestEnableTunRouting_WithoutIPv6TouchesNoIPv6Routes(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if f.has("interface ipv6") {
		t.Errorf("ipv6 routes installed in tun mode, got: %v", f.cmds)
	}
}

// Teardown must remove exactly what it added, including the IPv6 half — a
// leftover ::/1 through a dead adapter takes the machine's IPv6 with it.
func TestDisableTunRouting_RemovesIPv6Routes(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)
	original := f.state()

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}

	want := []string{
		"Remove-NetRoute 8000::/1 if 27 via fdfe:dcba:9876::1",
		"Remove-NetRoute ::/1 if 27 via fdfe:dcba:9876::1",
		"Remove-NetRoute 2a00:1450::64/128 if 12 via fe80::1",
		"Remove-NetRoute 128.0.0.0/1 if 27 via 10.0.0.1",
		"Remove-NetRoute 0.0.0.0/1 if 27 via 10.0.0.1",
		"Remove-NetRoute 45.150.32.235/32 if 12 via 192.168.31.1",
	}
	if got := f.removals(); !slices.Equal(got, want) {
		t.Errorf("removals = %v, want %v", got, want)
	}
	if f.state() != original {
		t.Errorf("table not restored:\nbefore %s\nafter  %s", original, f.state())
	}
	if installed != nil {
		t.Errorf("ownership not cleared: %v", installed)
	}
}

// ConvertTo-Json prints a lone object without the array brackets and nothing
// at all when no object came through. Both shapes must read as a list, and
// silence must not pass for an empty answer.
func TestDecodePS_ReadsBothShapes(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    int
		wantErr bool
	}{
		{name: "lone object", out: `{"NextHop":"192.168.31.1","InterfaceIndex":12}`, want: 1},
		{name: "array", out: `[{"NextHop":"192.168.31.1","InterfaceIndex":12},{"NextHop":"::","InterfaceIndex":27}]`, want: 2},
		{name: "byte order mark", out: "\xef\xbb\xbf[{\"NextHop\":\"::\",\"InterfaceIndex\":27}]\r\n", want: 1},
		{name: "nothing", out: "\r\n", wantErr: true},
		{name: "text", out: "192.168.31.1 12\r\n", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodePS[winRoute]([]byte(c.out))
			if c.wantErr {
				if err == nil {
					t.Fatalf("read %q as %+v", c.out, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodePS: %v", err)
			}
			if len(got) != c.want || got[0].InterfaceIndex == 0 {
				t.Errorf("got %+v, want %d routes with their fields", got, c.want)
			}
		})
	}
}
