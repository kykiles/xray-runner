//go:build linux

package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeIP models the `ip` binary over an in-memory host, so the routing logic
// is verified without root or touching the host's routing table. `route get`
// answers with a canned line, and the `-j` queries print the host state the
// way iproute2 6.19 does. add and del change that state by the kernel's rules
// as observed in a network namespace: add refuses a taken family/table/prefix/
// metric; del removes the lowest-metric entry matching every selector given
// and, like the kernel's IPv6 delete, ignores the route type.
type fakeIP struct {
	missing     bool
	routeGetOut string            // fallback answer for `ip route get`
	routeGets   map[string]string // per-destination answers
	routeGetErr error

	routes []fakeRoute
	rules  []fakeRule
	addrs  string // `ip -j -N addr show dev xray-tun`; empty is cleanAddrs

	raw      map[string]string // replaces a query's output; keyed "route4", "route6", "rule"
	queryErr map[string]error  // fails a query; keyed "route4", "route6", "rule", "addr"
	// before runs ahead of every mutation — a client racing the preflight;
	// fail makes the n-th mutation (1-based) fail when it returns true.
	before func(cmd string)
	fail   func(n int, cmd string) bool

	cmds []string // every mutation tried, failed ones included
}

type fakeRoute struct {
	v6     bool
	typ    string // as `ip -N` prints it: "" unicast, "2" local, "7" unreachable
	dst    netip.Prefix
	via    string
	dev    string
	table  string // "" is main
	metric int
}

type fakeRule struct {
	pref           int
	fwmark, fwmask string // hex, as ip prints them
	table          string
}

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

// cleanHost is a machine right after xray brought the tun up and before any
// routing: a default route and LAN on wlp3s0, the tun's own prefixes, the
// three stock rules.
func cleanHost() *fakeIP {
	return &fakeIP{
		routeGetOut: wanRouteGet,
		routes: []fakeRoute{
			{dst: pfx("0.0.0.0/0"), via: "192.168.31.1", dev: "wlp3s0"},
			{dst: pfx("10.0.0.0/24"), dev: "xray-tun"},
			{dst: pfx("192.168.31.0/24"), dev: "wlp3s0"},
			{typ: "2", dst: pfx("10.0.0.1/32"), dev: "xray-tun", table: "255"},
			{v6: true, dst: pfx("fdfe:dcba:9876::/126"), dev: "xray-tun", metric: 256},
			{v6: true, dst: pfx("fe80::/64"), dev: "wlp3s0", metric: 256},
			{v6: true, dst: pfx("::/0"), via: "fe80::1", dev: "wlp3s0", metric: 1024},
		},
		rules: []fakeRule{{pref: 0, table: "255"}, {pref: 32766, table: "254"}, {pref: 32767, table: "253"}},
	}
}

const cleanAddrs = `[{"ifindex":7,"ifname":"xray-tun","addr_info":[` +
	`{"family":"inet","local":"10.0.0.1","prefixlen":24,"scope":"global"},` +
	`{"family":"inet6","local":"fdfe:dcba:9876::1","prefixlen":126,"scope":"global"}]}]`

func (f *fakeIP) lookPath(bin string) error {
	if f.missing {
		return errors.New("not found")
	}
	return nil
}

func (f *fakeIP) run(bin string, args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "route" && args[1] == "get" {
		if out, ok := f.routeGets[args[2]]; ok {
			return []byte(out), nil
		}
		return []byte(f.routeGetOut), f.routeGetErr
	}
	if len(args) > 0 && args[0] == "-j" {
		return f.query(strings.Join(args, " "))
	}
	cmd := bin + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	if f.before != nil {
		f.before(cmd)
	}
	if f.fail != nil && f.fail(len(f.cmds), cmd) {
		return []byte("RTNETLINK answers: Operation not permitted\n"), errors.New("exit status 2")
	}
	return f.mutate(args)
}

// query answers a read-only `ip -j` call. A query the fake does not know
// fails, so the code under test cannot lean on output nobody modelled.
func (f *fakeIP) query(q string) ([]byte, error) {
	var key string
	var out any
	switch q {
	case "-j -N -4 route show table all":
		key, out = "route4", f.routeJSON(false)
	case "-j -N -6 route show table all":
		key, out = "route6", f.routeJSON(true)
	case "-j -N -4 rule show":
		key, out = "rule", f.ruleJSON()
	case "-j -N addr show dev xray-tun":
		addrs := f.addrs
		if addrs == "" {
			addrs = cleanAddrs
		}
		key, out = "addr", json.RawMessage(addrs)
	default:
		return nil, fmt.Errorf("fakeIP: unmodelled query %q", q)
	}
	if err := f.queryErr[key]; err != nil {
		return []byte("Error: query failed"), err
	}
	if raw, ok := f.raw[key]; ok {
		return []byte(raw), nil
	}
	return json.Marshal(out)
}

// routeJSON prints one family the way `ip -j -N route show table all` does:
// no "table" key for main, "default" for /0, a host route without its length,
// no metric when it is zero.
func (f *fakeIP) routeJSON(v6 bool) []map[string]any {
	out := []map[string]any{}
	for _, r := range f.routes {
		if r.v6 != v6 {
			continue
		}
		dst := r.dst.String()
		switch {
		case r.dst.Bits() == 0:
			dst = "default"
		case r.dst.IsSingleIP():
			dst = r.dst.Addr().String()
		}
		m := map[string]any{"dst": dst, "flags": []string{}}
		if r.typ != "" {
			m["type"] = r.typ
		}
		if r.via != "" {
			m["gateway"] = r.via
		}
		if r.dev != "" {
			m["dev"] = r.dev
		}
		if r.table != "" {
			m["table"] = r.table
		}
		if r.metric != 0 {
			m["metric"] = r.metric
		}
		out = append(out, m)
	}
	return out
}

func (f *fakeIP) ruleJSON() []map[string]any {
	out := []map[string]any{}
	for _, r := range f.rules {
		m := map[string]any{"priority": r.pref, "src": "all"}
		if r.fwmark != "" {
			m["fwmark"] = r.fwmark
		}
		if r.fwmask != "" {
			m["fwmask"] = r.fwmask
		}
		if r.table != "" {
			m["table"] = r.table
		}
		out = append(out, m)
	}
	return out
}

// state prints everything the routing code reads, for before/after comparisons.
func (f *fakeIP) state() string {
	b, _ := json.Marshal([]any{f.routeJSON(false), f.routeJSON(true), f.ruleJSON()})
	return string(b)
}

// tunDied drops what goes away with the tun device when xray exits.
func (f *fakeIP) tunDied() {
	f.routes = slices.DeleteFunc(f.routes, func(r fakeRoute) bool { return r.dev == "xray-tun" })
}

func (f *fakeIP) mutate(args []string) ([]byte, error) {
	v6 := len(args) > 0 && args[0] == "-6"
	if v6 {
		args = args[1:]
	}
	if len(args) >= 2 && (args[1] == "add" || args[1] == "del") {
		switch {
		case args[0] == "route":
			r, err := parseFakeRoute(v6, args[2:])
			if err != nil {
				return nil, err
			}
			if args[1] == "add" {
				return f.addRoute(r)
			}
			return f.delRoute(r)
		case args[0] == "rule" && !v6:
			r, err := parseFakeRule(args[2:])
			if err != nil {
				return nil, err
			}
			if args[1] == "add" {
				return f.addRule(r)
			}
			return f.delRule(r)
		}
	}
	return nil, fmt.Errorf("fakeIP: unmodelled command %q", args)
}

func parseFakeRoute(v6 bool, args []string) (fakeRoute, error) {
	r := fakeRoute{v6: v6}
	if len(args) > 0 && args[0] == "unreachable" {
		r.typ, args = "7", args[1:]
	}
	if len(args)%2 != 1 {
		return r, fmt.Errorf("fakeIP: unmodelled route %q", args)
	}
	dst, err := netip.ParsePrefix(args[0])
	if err != nil {
		return r, fmt.Errorf("fakeIP: destination %q: %w", args[0], err)
	}
	r.dst = dst
	for i := 1; i < len(args); i += 2 {
		switch v := args[i+1]; args[i] {
		case "via":
			r.via = v
		case "dev":
			r.dev = v
		case "table":
			r.table = v
		case "metric":
			if r.metric, err = strconv.Atoi(v); err != nil {
				return r, err
			}
		default:
			return r, fmt.Errorf("fakeIP: unmodelled route option %q", args[i])
		}
	}
	return r, nil
}

func parseFakeRule(args []string) (fakeRule, error) {
	var r fakeRule
	if len(args)%2 != 0 {
		return r, fmt.Errorf("fakeIP: unmodelled rule %q", args)
	}
	for i := 0; i < len(args); i += 2 {
		switch v := args[i+1]; args[i] {
		case "pref":
			pref, err := strconv.Atoi(v)
			if err != nil {
				return r, err
			}
			r.pref = pref
		case "fwmark":
			mark, err := strconv.ParseUint(v, 0, 32)
			if err != nil {
				return r, err
			}
			r.fwmark = fmt.Sprintf("%#x", mark)
		case "lookup":
			r.table = v
		default:
			return r, fmt.Errorf("fakeIP: unmodelled rule option %q", args[i])
		}
	}
	return r, nil
}

func (f *fakeIP) addRoute(r fakeRoute) ([]byte, error) {
	if r.v6 && r.metric == 0 {
		r.metric = 1024 // the kernel's default for a user IPv6 route
	}
	if r.typ == "7" && r.dev == "" {
		r.dev = "lo"
	}
	for _, x := range f.routes {
		if x.v6 == r.v6 && x.table == r.table && x.dst == r.dst && x.metric == r.metric {
			return []byte("RTNETLINK answers: File exists\n"), errors.New("exit status 2")
		}
	}
	f.routes = append(f.routes, r)
	return nil, nil
}

func (f *fakeIP) delRoute(sel fakeRoute) ([]byte, error) {
	best := -1
	for i, x := range f.routes {
		if x.v6 != sel.v6 || x.table != sel.table || x.dst != sel.dst ||
			sel.via != "" && x.via != sel.via ||
			sel.dev != "" && x.dev != sel.dev ||
			sel.metric != 0 && x.metric != sel.metric {
			continue
		}
		if best < 0 || x.metric < f.routes[best].metric {
			best = i
		}
	}
	if best < 0 {
		return []byte("RTNETLINK answers: No such process\n"), errors.New("exit status 2")
	}
	f.routes = slices.Delete(f.routes, best, best+1)
	return nil, nil
}

// addRule refuses an identical rule at the same priority and files the new
// one after its equals, as the kernel does.
func (f *fakeIP) addRule(r fakeRule) ([]byte, error) {
	if slices.Contains(f.rules, r) {
		return []byte("RTNETLINK answers: File exists\n"), errors.New("exit status 2")
	}
	i := slices.IndexFunc(f.rules, func(x fakeRule) bool { return x.pref > r.pref })
	if i < 0 {
		i = len(f.rules)
	}
	f.rules = slices.Insert(f.rules, i, r)
	return nil, nil
}

func (f *fakeIP) delRule(r fakeRule) ([]byte, error) {
	i := slices.Index(f.rules, r)
	if i < 0 {
		return []byte("RTNETLINK answers: No such file or directory\n"), errors.New("exit status 2")
	}
	f.rules = slices.Delete(f.rules, i, i+1)
	return nil, nil
}

func (f *fakeIP) has(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// withFakeIP swaps in the fake and starts with nothing owned, so one test's
// routes never reach the next one's teardown.
func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	origCmd, origInstalled := ipCmd, installed
	ipCmd, installed = f, nil
	t.Cleanup(func() { ipCmd, installed = origCmd, origInstalled })
}

// wanRouteGet is what `ip route get <server>` prints for a server reachable
// through the physical gateway.
const wanRouteGet = "45.150.32.235 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "

var tunCfg = TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"}}

// Without these two routes the tun device exists but no traffic ever enters
// it — the bug this whole file addresses.
func TestEnableTunRouting_SendsDefaultTrafficIntoTun(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 0.0.0.0/1 dev xray-tun") {
		t.Errorf("missing lower split-default route, got: %v", f.cmds)
	}
	if !f.has("route add 128.0.0.0/1 dev xray-tun") {
		t.Errorf("missing upper split-default route, got: %v", f.cmds)
	}
}

// The tunnel's own packets to the VPS must keep using the physical path,
// otherwise xray's uplink routes into the tun it is serving and deadlocks.
func TestEnableTunRouting_ExcludesServerViaPhysicalGateway(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 45.150.32.235/32 via 192.168.31.1 dev wlp3s0") {
		t.Errorf("missing server exclusion route, got: %v", f.cmds)
	}
}

// Traffic the panel's routing sends out `direct` leaves xray through a freedom
// outbound carrying the firewall mark. Without a rule lifting those packets out
// of the split default they re-enter the tun they just left and loop forever.
func TestEnableTunRouting_LiftsMarkedTrafficOutOfTheTunnel(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 0.0.0.0/0 via 192.168.31.1 dev wlp3s0 table "+directTable) ||
		!f.has("rule add pref 32765 fwmark 255 lookup "+directTable) {
		t.Errorf("marked traffic has no physical path, got: %v", f.cmds)
	}
}

// The rule must be in place before the split default exists, or direct traffic
// loops during the window between the two.
func TestEnableTunRouting_InstallsMarkRuleBeforeSplitDefault(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	rule, split := -1, -1
	for i, c := range f.cmds {
		if strings.Contains(c, "rule add") {
			rule = i
		}
		if strings.Contains(c, "route add 0.0.0.0/1") {
			split = i
		}
	}
	if rule < 0 || split < 0 || rule > split {
		t.Errorf("mark rule (%d) must come before split default (%d): %v", rule, split, f.cmds)
	}
}

// The mark rule goes where the kernel files a rule given no priority — just
// past `lookup local`, ahead of other clients' rules — but by number, so the
// teardown can name it. With no number free there, nothing is installed.
func TestEnableTunRouting_PlacesMarkRuleAheadOfOtherRules(t *testing.T) {
	t.Run("ahead of another client", func(t *testing.T) {
		f := cleanHost()
		f.addRule(fakeRule{pref: 5210, table: "52"})
		withFakeIP(t, f)

		if err := EnableTunRouting(tunCfg); err != nil {
			t.Fatalf("EnableTunRouting: %v", err)
		}
		if !f.has("rule add pref 5209 fwmark 255 lookup " + directTable) {
			t.Errorf("mark rule not placed ahead of the client's rule: %v", f.cmds)
		}
	})
	t.Run("no room", func(t *testing.T) {
		f := cleanHost()
		f.addRule(fakeRule{pref: 1, table: "52"})
		withFakeIP(t, f)

		if err := EnableTunRouting(tunCfg); err == nil {
			t.Fatal("placed the mark rule without a free priority")
		}
		if len(f.cmds) != 0 {
			t.Errorf("changed routing without a place for the rule: %v", f.cmds)
		}
	})
}

// A leftover ip rule outlives the process and keeps steering marked traffic
// into table 8888. The teardown names the rule and the direct route exactly:
// flushing the table would take any other client's routes there with it.
func TestDisableTunRouting_RemovesExactlyTheMarkRuleAndDirectRoute(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)
	clean := f.state()

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, want := range []string{
		"ip rule del pref 32765 fwmark 255 lookup " + directTable,
		"ip route del 0.0.0.0/0 via 192.168.31.1 dev wlp3s0 table " + directTable,
	} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
	if f.has("flush") {
		t.Errorf("teardown flushed a table: %v", f.cmds)
	}
	if got := f.state(); got != clean {
		t.Errorf("teardown left the host changed:\nwant %s\ngot  %s", clean, got)
	}
}

// A balancer profile rotates across all its servers, so every one of them must
// stay on the physical path — not just the first.
func TestEnableTunRouting_ExcludesEveryServerOfBalancerProfile(t *testing.T) {
	f := cleanHost()
	f.routeGets = map[string]string{
		"45.150.32.235": wanRouteGet,
		"203.0.113.7":   "203.0.113.7 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 ",
		markProbe:       wanRouteGet,
	}
	withFakeIP(t, f)

	cfg := TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235", "203.0.113.7"}}
	if err := EnableTunRouting(cfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	for _, want := range []string{
		"route add 45.150.32.235/32 via 192.168.31.1 dev wlp3s0",
		"route add 203.0.113.7/32 via 192.168.31.1 dev wlp3s0",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}

	f.cmds = nil
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, want := range []string{
		"route del 45.150.32.235/32 via 192.168.31.1 dev wlp3s0",
		"route del 203.0.113.7/32 via 192.168.31.1 dev wlp3s0",
	} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A server on the same L2 segment has no gateway; the route must still pin the
// device, without a bogus `via`.
func TestEnableTunRouting_ExcludesServerOnLocalLink(t *testing.T) {
	f := cleanHost()
	f.routeGetOut = "192.168.31.5 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "
	withFakeIP(t, f)

	if err := EnableTunRouting(TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"192.168.31.5"}}); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route add 192.168.31.5/32 dev wlp3s0") {
		t.Errorf("missing on-link server exclusion, got: %v", f.cmds)
	}
	if f.has("via") {
		t.Errorf("on-link route must not carry a gateway, got: %v", f.cmds)
	}
}

// Installing the split default before knowing the server's physical path would
// blackhole the tunnel's own uplink, so this must fail before touching routes.
func TestEnableTunRouting_FailsWithoutTouchingRoutesWhenServerPathUnknown(t *testing.T) {
	f := cleanHost()
	f.routeGetErr = errors.New("network is unreachable")
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the server's physical route can't be resolved")
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)
	clean := f.state()

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if f.state() == clean {
		t.Fatal("EnableTunRouting changed nothing")
	}
	f.cmds = nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, want := range []string{
		"route del 0.0.0.0/1 dev xray-tun",
		"route del 128.0.0.0/1 dev xray-tun",
		"route del 45.150.32.235/32 via 192.168.31.1 dev wlp3s0",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
	if got := f.state(); got != clean {
		t.Errorf("teardown left the host changed:\nwant %s\ngot  %s", clean, got)
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not fire stray `route del` at the host.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}

	if len(f.cmds) != 0 {
		t.Errorf("expected no commands, got: %v", f.cmds)
	}
}

// withIPv6Stack points the stack check at a file that exists or not.
func withIPv6Stack(t *testing.T, present bool) {
	t.Helper()
	orig := ifInet6
	ifInet6 = t.TempDir() + "/if_inet6"
	if present {
		if err := os.WriteFile(ifInet6, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { ifInet6 = orig })
}

var tunCfg6 = TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", Addr6: "fdfe:dcba:9876::1", ServerIPs: []string{"45.150.32.235"}}

// A06/A11: the VPN runs without IPv6, and tun used to leave it alone — on a
// dual-stack network a v6-capable app went around the tunnel with its real
// address. Unreachable, not blackhole: the app is told at once and falls back
// to IPv4, which the tunnel carries, instead of hanging until a timeout.
func TestEnableTunRouting_BlocksIPv6(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	for _, want := range []string{
		"-6 route add unreachable ::/1 dev lo metric 1024",
		"-6 route add unreachable 8000::/1 dev lo metric 1024",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}

	f.cmds = nil
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, want := range []string{
		"-6 route del unreachable ::/1 dev lo metric 1024",
		"-6 route del unreachable 8000::/1 dev lo metric 1024",
	} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A kernel booted without IPv6 has nothing to leak and no table to write to.
func TestEnableTunRouting_SkipsIPv6BlockWithoutStack(t *testing.T) {
	withIPv6Stack(t, false)
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if f.has("-6") {
		t.Errorf("IPv6 commands on a host without IPv6: %v", f.cmds)
	}
}

func TestEnableTunRouting_NoIPv6BlockWithoutAddr6(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if f.has("-6") {
		t.Errorf("IPv6 commands without Addr6: %v", f.cmds)
	}
}

func TestParseRouteGet(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		via     string
		dev     string
		wantErr bool
	}{
		{name: "via gateway", out: wanRouteGet, via: "192.168.31.1", dev: "wlp3s0"},
		{name: "on-link", out: "192.168.31.5 dev wlp3s0 src 192.168.31.94 ", via: "", dev: "wlp3s0"},
		{name: "no dev", out: "45.150.32.235 via 192.168.31.1 ", wantErr: true},
		{name: "empty", out: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			via, dev, err := parseRouteGet(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouteGet: %v", err)
			}
			if via != tt.via || dev != tt.dev {
				t.Errorf("got via=%q dev=%q, want via=%q dev=%q", via, dev, tt.via, tt.dev)
			}
		})
	}
}

// A crash leaves the routes behind: nothing runs the teardown, and only the /1
// halves go away with the tun device. The next start cannot tell those entries
// from another client's — the bytes match either way — so it refuses them,
// naming each, instead of taking them over, and changes nothing.
func TestEnableTunRouting_RefusesLeftoversFromACrashedRun(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("first EnableTunRouting: %v", err)
	}
	installed = nil // the process died: nothing remembers the routes
	f.tunDied()
	f.cmds = nil
	before := f.state()

	err := EnableTunRouting(tunCfg6)
	if err == nil {
		t.Fatalf("took over a crashed run's leftovers: %v", f.cmds)
	}
	for _, want := range []string{"45.150.32.235/32", "таблица 8888", "fwmark 0xff", "::/1", "8000::/1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	if len(f.cmds) != 0 || f.state() != before {
		t.Errorf("changed routing despite the conflict: %v", f.cmds)
	}
}

// A08: every entry EnableTunRouting creates must be absent before it starts.
// Taking a foreign one over would let the teardown delete it afterwards.
func TestEnableTunRouting_RefusesEntriesItWouldCreate(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *fakeIP)
		want    string // what the error must name
	}{
		{"server /32 already routed", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{dst: pfx("45.150.32.235/32"), via: "192.168.31.1", dev: "wlp3s0"})
		}, "45.150.32.235/32"},
		{"server /32 on another interface and metric", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{dst: pfx("45.150.32.235/32"), dev: "wg0", metric: 100})
		}, "45.150.32.235/32 dev wg0"},
		{"lower split half", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{dst: pfx("0.0.0.0/1"), dev: "wg0"})
		}, "0.0.0.0/1"},
		{"upper split half", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{dst: pfx("128.0.0.0/1"), dev: "tun0"})
		}, "128.0.0.0/1"},
		{"direct table not empty", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{dst: pfx("10.9.0.0/16"), dev: "wg0", table: "8888"})
		}, "таблица 8888"},
		{"another rule for the direct mark", func(f *fakeIP) {
			f.addRule(fakeRule{pref: 100, fwmark: "0xff", table: "100"})
		}, "fwmark 0xff"},
		{"masked rule catching the direct mark", func(f *fakeIP) {
			f.addRule(fakeRule{pref: 100, fwmark: "0x1", fwmask: "0x1", table: "100"})
		}, "fwmark 0x1/0x1"},
		{"rule into the direct table", func(f *fakeIP) {
			f.addRule(fakeRule{pref: 200, table: "8888"})
		}, "lookup 8888"},
		{"ipv6 half", func(f *fakeIP) {
			f.routes = append(f.routes, fakeRoute{v6: true, typ: "7", dst: pfx("::/1"), dev: "lo", metric: 1024})
		}, "::/1"},
		{"tun carrying a foreign address", func(f *fakeIP) {
			f.addrs = `[{"ifindex":7,"ifname":"xray-tun","addr_info":[{"family":"inet","local":"10.8.0.2","prefixlen":24}]}]`
		}, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIPv6Stack(t, true)
			f := cleanHost()
			tt.prepare(f)
			withFakeIP(t, f)

			err := EnableTunRouting(tunCfg6)
			if err == nil {
				t.Fatalf("routed over an existing %s: %v", tt.want, f.cmds)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error does not name %q: %v", tt.want, err)
			}
			if len(f.cmds) != 0 {
				t.Errorf("changed routing despite the conflict: %v", f.cmds)
			}
		})
	}
}

// A query that fails or prints something unreadable leaves the host's state
// unknown, and routing on a guess is how foreign entries got overwritten.
func TestEnableTunRouting_RefusesWhenStateIsUnknown(t *testing.T) {
	failed := errors.New("exit status 1")
	tests := []struct {
		name    string
		prepare func(f *fakeIP)
	}{
		{"routes unreadable", func(f *fakeIP) { f.queryErr = map[string]error{"route4": failed} }},
		{"ipv6 routes unreadable", func(f *fakeIP) { f.queryErr = map[string]error{"route6": failed} }},
		{"rules unreadable", func(f *fakeIP) { f.queryErr = map[string]error{"rule": failed} }},
		{"tun gone", func(f *fakeIP) { f.queryErr = map[string]error{"addr": failed} }},
		{"garbled output", func(f *fakeIP) { f.raw = map[string]string{"route4": "Dump terminated"} }},
		{"unknown destination", func(f *fakeIP) { f.raw = map[string]string{"route4": `[{"dst":"somewhere","dev":"wg0"}]`} }},
		{"unknown fwmark", func(f *fakeIP) {
			f.raw = map[string]string{"rule": `[{"priority":100,"fwmark":"mark","table":"100"}]`}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIPv6Stack(t, true)
			f := cleanHost()
			tt.prepare(f)
			withFakeIP(t, f)

			if err := EnableTunRouting(tunCfg6); err == nil {
				t.Fatalf("routed without knowing the host's state: %v", f.cmds)
			}
			if len(f.cmds) != 0 {
				t.Errorf("changed routing on unknown state: %v", f.cmds)
			}
		})
	}
}

// The default route, the LAN and other clients' more specific routes are not
// what EnableTunRouting creates: they neither block it nor get touched, and
// the teardown leaves the host as it found it.
func TestEnableTunRouting_LeavesUnrelatedEntriesAlone(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	f.routes = append(f.routes,
		fakeRoute{dst: pfx("45.150.32.0/24"), via: "10.9.0.1", dev: "wg0"},
		fakeRoute{dst: pfx("45.150.32.235/32"), dev: "wg0", table: "100"},
		fakeRoute{dst: pfx("198.51.100.9/32"), dev: "wg0"},
		fakeRoute{v6: true, dst: pfx("2000::/3"), dev: "wg0", metric: 1024},
		fakeRoute{v6: true, dst: pfx("::/1"), dev: "wg0", table: "8888", metric: 1024})
	f.addRule(fakeRule{pref: 100, fwmark: "0x100", table: "100"})
	withFakeIP(t, f)
	before := f.state()

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "wg0") || strings.Contains(c, "table 100") {
			t.Errorf("touched a foreign entry: %q", c)
		}
	}
	if got := f.state(); got != before {
		t.Errorf("host changed:\nwant %s\ngot  %s", before, got)
	}
}

// The preflight cannot see a client that routes a server right after it: add
// then fails on the taken prefix instead of replacing it, what this call had
// already added is taken back, and the client's route stays.
func TestEnableTunRouting_FailsWhenAClientRacesThePreflight(t *testing.T) {
	f := cleanHost()
	theirs := fakeRoute{dst: pfx("203.0.113.7/32"), dev: "wg0"}
	f.before = func(string) {
		if len(f.cmds) == 1 {
			f.routes = append(f.routes, theirs)
		}
	}
	withFakeIP(t, f)

	cfg := TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235", "203.0.113.7"}}
	err := EnableTunRouting(cfg)
	if err == nil {
		t.Fatalf("routed over the client's route: %v", f.cmds)
	}
	if !strings.Contains(err.Error(), "203.0.113.7") {
		t.Errorf("error does not name the taken server: %v", err)
	}
	want := cleanHost()
	want.routes = append(want.routes, theirs)
	if got := f.state(); got != want.state() {
		t.Errorf("rollback left the host changed:\nwant %s\ngot  %s", want.state(), got)
	}
	if f.has("route del 203.0.113.7") {
		t.Errorf("deleted the client's route: %v", f.cmds)
	}
}

// Whichever add fails, everything this call added before it is taken back —
// once each — and the entry that failed is never deleted: it is not ours.
func TestEnableTunRouting_RollsBackEveryFailure(t *testing.T) {
	withIPv6Stack(t, true)
	probe := cleanHost()
	withFakeIP(t, probe)
	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	adds := len(probe.cmds)

	for n := 1; n <= adds; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			f := cleanHost()
			f.fail = func(i int, _ string) bool { return i == n }
			withFakeIP(t, f)
			clean := f.state()

			if err := EnableTunRouting(tunCfg6); err == nil {
				t.Fatalf("add %d failed and Enable succeeded", n)
			}
			if got := f.state(); got != clean {
				t.Errorf("rollback left the host changed:\nwant %s\ngot  %s", clean, got)
			}
			var dels int
			for _, c := range f.cmds {
				if strings.Contains(c, " del ") {
					dels++
				}
			}
			if dels != n-1 {
				t.Errorf("%d deletes after %d successful adds: %v", dels, n-1, f.cmds)
			}
		})
	}
}

// Cleanup is safe to repeat: a second call owns nothing and sends nothing —
// not even for entries that merely look like the ones it removed.
func TestDisableTunRouting_IsNotRepeated(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	f.routes = append(f.routes, fakeRoute{dst: pfx("45.150.32.235/32"), via: "192.168.31.1", dev: "wlp3s0"})
	f.cmds = nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("second DisableTunRouting: %v", err)
	}
	if len(f.cmds) != 0 {
		t.Errorf("second teardown sent commands: %v", f.cmds)
	}
}

// When xray exits the tun takes the split default with it. The teardown finds
// those routes gone, which is success, and still removes the rest.
func TestDisableTunRouting_AfterTheTunIsGone(t *testing.T) {
	withIPv6Stack(t, true)
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.tunDied()
	f.cmds = nil

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	if f.has("0.0.0.0/1") || f.has("128.0.0.0/1") {
		t.Errorf("deleted routes that were already gone: %v", f.cmds)
	}
	want := cleanHost()
	want.tunDied()
	if got := f.state(); got != want.state() {
		t.Errorf("teardown left entries behind:\nwant %s\ngot  %s", want.state(), got)
	}
}

// A client that replaced one of the entries after Enable owns it now: the
// teardown leaves it in place instead of deleting the prefix outright. The
// IPv6 case matters most — the kernel's IPv6 delete ignores the route type, so
// `del unreachable ::/1` alone removes a unicast route sitting there.
func TestDisableTunRouting_LeavesAReplacedEntryAlone(t *testing.T) {
	tests := []struct {
		name         string
		ours, theirs fakeRoute
	}{
		{"server route",
			fakeRoute{dst: pfx("45.150.32.235/32"), via: "192.168.31.1", dev: "wlp3s0"},
			fakeRoute{dst: pfx("45.150.32.235/32"), dev: "wg0"}},
		{"ipv6 half",
			fakeRoute{v6: true, typ: "7", dst: pfx("::/1"), dev: "lo", metric: 1024},
			fakeRoute{v6: true, dst: pfx("::/1"), dev: "wg0", metric: 1024}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIPv6Stack(t, true)
			f := cleanHost()
			withFakeIP(t, f)

			if err := EnableTunRouting(tunCfg6); err != nil {
				t.Fatalf("EnableTunRouting: %v", err)
			}
			i := slices.Index(f.routes, tt.ours)
			if i < 0 {
				t.Fatalf("EnableTunRouting did not add %+v", tt.ours)
			}
			f.routes[i] = tt.theirs

			if err := DisableTunRouting(); err != nil {
				t.Fatalf("DisableTunRouting: %v", err)
			}
			if !slices.Contains(f.routes, tt.theirs) {
				t.Errorf("deleted the client's route: %v", f.cmds)
			}
		})
	}
}

// A delete that fails leaves the entry owned and says so instead of calling
// the host clean. The next Enable finishes that teardown before it adds
// anything, so nothing is stacked on top and nothing is forgotten.
func TestDisableTunRouting_KeepsWhatItFailedToRemove(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)
	clean := f.state()

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.fail = func(_ int, cmd string) bool { return strings.Contains(cmd, "route del 45.150.32.235/32") }
	err := DisableTunRouting()
	if err == nil || !strings.Contains(err.Error(), "45.150.32.235/32") {
		t.Fatalf("a failed delete passed for a clean teardown: %v", err)
	}

	f.fail = nil
	f.cmds = nil
	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting after a failed teardown: %v", err)
	}
	if !f.has("route del 45.150.32.235/32 via 192.168.31.1 dev wlp3s0") {
		t.Errorf("the failed delete was not retried: %v", f.cmds)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	if got := f.state(); got != clean {
		t.Errorf("host not clean after the retry:\nwant %s\ngot  %s", clean, got)
	}
}

// After a full teardown the next Enable asks for the physical path again: the
// uplink may have changed, and nothing of the last session is reused.
func TestEnableTunRouting_ResolvesThePhysicalPathAfresh(t *testing.T) {
	f := cleanHost()
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	f.routeGetOut = "45.150.32.235 via 192.168.31.254 dev eth0 src 192.168.31.94 uid 0 "
	f.cmds = nil

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("second EnableTunRouting: %v", err)
	}
	if !f.has("route add 45.150.32.235/32 via 192.168.31.254 dev eth0") {
		t.Errorf("the new uplink was not used: %v", f.cmds)
	}
}
