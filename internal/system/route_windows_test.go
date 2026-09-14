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
// pipeline item. A script the fake does not know fails, so the code under test
// cannot lean on output nobody modelled.
type fakeIP struct {
	routes   []fakeRoute
	tunIndex int // ifIndex of the xray-tun adapter; zero means there is none

	bindAlias  string
	bindErr    error
	bindScript string
	findScript string

	raw      map[string]string // replaces a script's output; keyed "find", "adapter", "routes"
	queryErr map[string]error  // fails a script; same keys

	scripts []string // every script run
	cmds    []string // every mutating command tried
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
	f.cmds = append(f.cmds, bin+" "+strings.Join(args, " "))
	return f.mutate(bin, args)
}

var findTarget = regexp.MustCompile(`Find-NetRoute -RemoteIPAddress '([^']*)'`)

func (f *fakeIP) script(s string) ([]byte, error) {
	f.scripts = append(f.scripts, s)
	// Checked first: the DirectBind script names both cmdlets.
	if strings.Contains(s, "InterfaceAlias") {
		f.bindScript = s
		return []byte(f.bindAlias), f.bindErr
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
		return f.add(fakeRoute{dst: dst, nextHop: args[4], ifIndex: index})
	// route delete DEST mask MASK: without a gateway or interface it removes
	// every route to the prefix, another client's included.
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
			if hop == "" {
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

func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	orig := ipCmd
	ipCmd = f
	t.Cleanup(func() {
		ipCmd = orig
		installed, installedV6 = nil, nil
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

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the server's physical route can't be resolved")
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
		installed, installedV6 = nil, nil
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

func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	DisableTunRouting()

	for _, want := range []string{
		"route delete 0.0.0.0 mask 128.0.0.0",
		"route delete 128.0.0.0 mask 128.0.0.0",
		"route delete 45.150.32.235 mask 255.255.255.255",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not fire stray `route delete` at the host.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := winHost()
	withFakeIP(t, f)

	DisableTunRouting()

	if len(f.cmds) != 0 {
		t.Errorf("expected no commands, got: %v", f.cmds)
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

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}

	for _, prefix := range []string{"::/1", "8000::/1", "2a00:1450::64/128"} {
		if !f.has("netsh interface ipv6 delete route prefix=" + prefix) {
			t.Errorf("IPv6 route %q not removed, got: %v", prefix, f.cmds)
		}
	}
	if installedV6 != nil {
		t.Errorf("installedV6 not cleared: %v", installedV6)
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
