//go:build linux

package system

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeIP models the `ip` binary: it answers `route get` with a canned line,
// answers the preflight's `-j` queries with the host state below and records
// every mutating command, so the routing logic can be verified without root or
// touching the host's routing table.
type fakeIP struct {
	missing     bool
	routeGetOut string            // fallback answer for `ip route get`
	routeGets   map[string]string // per-destination answers
	routeGetErr error
	// Host state as `ip -j -N` prints it; an empty field answers with the
	// matching clean* host below.
	routes4, routes6, rules, addrs string
	queryErr                       map[string]error // keyed "route4", "route6", "rule", "addr"
	cmds                           []string
}

// A clean host as `ip -j -N` prints it right after xray brought the tun up:
// main-table routes carry no "table" key, host routes print without their /32.
const (
	cleanRoutes4 = `[{"dst":"default","gateway":"192.168.31.1","dev":"wlp3s0","flags":[]},` +
		`{"dst":"10.0.0.0/24","dev":"xray-tun","protocol":"2","scope":"253","prefsrc":"10.0.0.1","flags":[]},` +
		`{"dst":"192.168.31.0/24","dev":"wlp3s0","protocol":"2","scope":"253","prefsrc":"192.168.31.94","flags":[]},` +
		`{"type":"2","dst":"10.0.0.1","dev":"xray-tun","table":"255","protocol":"2","scope":"254","prefsrc":"10.0.0.1","flags":[]}]`
	cleanRoutes6 = `[{"dst":"fdfe:dcba:9876::/126","dev":"xray-tun","protocol":"2","metric":256,"flags":[]},` +
		`{"dst":"fe80::/64","dev":"wlp3s0","protocol":"2","metric":256,"flags":[]},` +
		`{"dst":"default","gateway":"fe80::1","dev":"wlp3s0","metric":1024,"flags":[]}]`
	cleanRules = `[{"priority":0,"src":"all","table":"255"},` +
		`{"priority":32766,"src":"all","table":"254"},` +
		`{"priority":32767,"src":"all","table":"253"}]`
	cleanAddrs = `[{"ifindex":7,"ifname":"xray-tun","addr_info":[` +
		`{"family":"inet","local":"10.0.0.1","prefixlen":24,"scope":"global"},` +
		`{"family":"inet6","local":"fdfe:dcba:9876::1","prefixlen":126,"scope":"global"}]}]`
)

// plus prepends entries to a JSON array printed by `ip -j`.
func plus(list string, entries ...string) string {
	return "[" + strings.Join(entries, ",") + "," + list[1:]
}

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
	f.cmds = append(f.cmds, bin+" "+strings.Join(args, " "))
	return nil, nil
}

// query answers a read-only `ip -j` call. A query the fake does not know fails,
// so the code under test cannot lean on output nobody modelled.
func (f *fakeIP) query(q string) ([]byte, error) {
	var key, out, clean string
	switch q {
	case "-j -N -4 route show table all":
		key, out, clean = "route4", f.routes4, cleanRoutes4
	case "-j -N -6 route show table all":
		key, out, clean = "route6", f.routes6, cleanRoutes6
	case "-j -N -4 rule show":
		key, out, clean = "rule", f.rules, cleanRules
	case "-j -N addr show dev xray-tun":
		key, out, clean = "addr", f.addrs, cleanAddrs
	default:
		return nil, fmt.Errorf("fakeIP: unmodelled query %q", q)
	}
	if err := f.queryErr[key]; err != nil {
		return []byte("Error: query failed"), err
	}
	if out == "" {
		out = clean
	}
	return []byte(out), nil
}

func (f *fakeIP) has(substr string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func withFakeIP(t *testing.T, f *fakeIP) {
	t.Helper()
	orig := ipCmd
	ipCmd = f
	t.Cleanup(func() { ipCmd = orig })
}

// wanRouteGet is what `ip route get <server>` prints for a server reachable
// through the physical gateway.
const wanRouteGet = "45.150.32.235 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "

var tunCfg = TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"}}

// Without these two routes the tun device exists but no traffic ever enters
// it — the bug this whole file addresses.
func TestEnableTunRouting_SendsDefaultTrafficIntoTun(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route replace 0.0.0.0/1 dev xray-tun") {
		t.Errorf("missing lower split-default route, got: %v", f.cmds)
	}
	if !f.has("route replace 128.0.0.0/1 dev xray-tun") {
		t.Errorf("missing upper split-default route, got: %v", f.cmds)
	}
}

// The tunnel's own packets to the VPS must keep using the physical path,
// otherwise xray's uplink routes into the tun it is serving and deadlocks.
func TestEnableTunRouting_ExcludesServerViaPhysicalGateway(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route replace 45.150.32.235/32 via 192.168.31.1 dev wlp3s0") {
		t.Errorf("missing server exclusion route, got: %v", f.cmds)
	}
}

// Traffic the panel's routing sends out `direct` leaves xray through a freedom
// outbound carrying the firewall mark. Without a rule lifting those packets out
// of the split default they re-enter the tun they just left and loop forever.
func TestEnableTunRouting_LiftsMarkedTrafficOutOfTheTunnel(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route replace default via 192.168.31.1 dev wlp3s0 table "+directTable) ||
		!f.has("rule add fwmark 255 lookup "+directTable) {
		t.Errorf("marked traffic has no physical path, got: %v", f.cmds)
	}
}

// The rule must be in place before the split default exists, or direct traffic
// loops during the window between the two.
func TestEnableTunRouting_InstallsMarkRuleBeforeSplitDefault(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	rule, split := -1, -1
	for i, c := range f.cmds {
		if strings.Contains(c, "rule add fwmark") {
			rule = i
		}
		if strings.Contains(c, "route replace 0.0.0.0/1") {
			split = i
		}
	}
	if rule < 0 || split < 0 || rule > split {
		t.Errorf("mark rule (%d) must come before split default (%d): %v", rule, split, f.cmds)
	}
}

// A leftover ip rule survives the process and silently steers the host's
// traffic into an empty table on the next boot.
func TestDisableTunRouting_RemovesMarkRuleAndTable(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	DisableTunRouting()

	for _, want := range []string{
		"rule del fwmark 255 lookup " + directTable,
		"route flush table " + directTable,
	} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A balancer profile rotates across all its servers, so every one of them must
// stay on the physical path — not just the first.
func TestEnableTunRouting_ExcludesEveryServerOfBalancerProfile(t *testing.T) {
	f := &fakeIP{routeGets: map[string]string{
		"45.150.32.235": wanRouteGet,
		"203.0.113.7":   "203.0.113.7 via 192.168.31.1 dev wlp3s0 src 192.168.31.94 ",
		markProbe:       wanRouteGet,
	}}
	withFakeIP(t, f)

	cfg := TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235", "203.0.113.7"}}
	if err := EnableTunRouting(cfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	for _, want := range []string{
		"route replace 45.150.32.235/32 via 192.168.31.1 dev wlp3s0",
		"route replace 203.0.113.7/32 via 192.168.31.1 dev wlp3s0",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}

	f.cmds = nil
	DisableTunRouting()
	for _, want := range []string{"route del 45.150.32.235/32", "route del 203.0.113.7/32"} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A server on the same L2 segment has no gateway; the route must still pin the
// device, without a bogus `via`.
func TestEnableTunRouting_ExcludesServerOnLocalLink(t *testing.T) {
	f := &fakeIP{routeGetOut: "192.168.31.5 dev wlp3s0 src 192.168.31.94 uid 0 \n    cache "}
	withFakeIP(t, f)

	if err := EnableTunRouting(TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"192.168.31.5"}}); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	if !f.has("route replace 192.168.31.5/32 dev wlp3s0") {
		t.Errorf("missing on-link server exclusion, got: %v", f.cmds)
	}
	if f.has("via") {
		t.Errorf("on-link route must not carry a gateway, got: %v", f.cmds)
	}
}

// Installing the split default before knowing the server's physical path would
// blackhole the tunnel's own uplink, so this must fail before touching routes.
func TestEnableTunRouting_FailsWithoutTouchingRoutesWhenServerPathUnknown(t *testing.T) {
	f := &fakeIP{routeGetErr: errors.New("network is unreachable")}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err == nil {
		t.Fatal("expected error when the server's physical route can't be resolved")
	}
	if len(f.cmds) != 0 {
		t.Errorf("no routes may be installed on failure, got: %v", f.cmds)
	}
}

func TestDisableTunRouting_RemovesEverythingEnableAdded(t *testing.T) {
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	f.cmds = nil

	DisableTunRouting()

	for _, want := range []string{
		"route del 0.0.0.0/1 dev xray-tun",
		"route del 128.0.0.0/1 dev xray-tun",
		"route del 45.150.32.235/32",
	} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}
}

// Disable runs on every session teardown, including sessions that never
// enabled tun routing; it must not fire stray `route del` at the host.
func TestDisableTunRouting_NoopWhenNothingWasEnabled(t *testing.T) {
	f := &fakeIP{}
	withFakeIP(t, f)

	DisableTunRouting()

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
	f := &fakeIP{routeGetOut: wanRouteGet}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	for _, want := range []string{"-6 route replace unreachable ::/1", "-6 route replace unreachable 8000::/1"} {
		if !f.has(want) {
			t.Errorf("missing %q, got: %v", want, f.cmds)
		}
	}

	f.cmds = nil
	DisableTunRouting()
	for _, want := range []string{"-6 route del unreachable ::/1", "-6 route del unreachable 8000::/1"} {
		if !f.has(want) {
			t.Errorf("teardown missing %q, got: %v", want, f.cmds)
		}
	}
}

// A kernel booted without IPv6 has nothing to leak and no table to write to.
func TestEnableTunRouting_SkipsIPv6BlockWithoutStack(t *testing.T) {
	withIPv6Stack(t, false)
	f := &fakeIP{routeGetOut: wanRouteGet}
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
	f := &fakeIP{routeGetOut: wanRouteGet}
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
	f := &fakeIP{
		routeGetOut: wanRouteGet,
		routes4: plus(cleanRoutes4,
			`{"dst":"default","gateway":"192.168.31.1","dev":"wlp3s0","table":"8888","flags":[]}`,
			`{"dst":"45.150.32.235","gateway":"192.168.31.1","dev":"wlp3s0","flags":[]}`),
		routes6: plus(cleanRoutes6,
			`{"type":"7","dst":"::/1","dev":"lo","metric":1024,"flags":[]}`,
			`{"type":"7","dst":"8000::/1","dev":"lo","metric":1024,"flags":[]}`),
		rules: plus(cleanRules, `{"priority":32765,"src":"all","fwmark":"0xff","table":"8888"}`),
	}
	withFakeIP(t, f)

	err := EnableTunRouting(tunCfg6)
	if err == nil {
		t.Fatalf("took over a crashed run's leftovers: %v", f.cmds)
	}
	for _, want := range []string{"45.150.32.235/32", "таблица 8888", "fwmark 0xff", "::/1", "8000::/1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	if len(f.cmds) != 0 {
		t.Errorf("changed routing despite the conflict: %v", f.cmds)
	}
}

// A08: every entry EnableTunRouting creates must be absent before it starts.
// replace would silently take a foreign one over, and the teardown would then
// delete it outright.
func TestEnableTunRouting_RefusesEntriesItWouldCreate(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(f *fakeIP)
		want    string // what the error must name
	}{
		{"server /32 already routed", func(f *fakeIP) {
			f.routes4 = plus(cleanRoutes4, `{"dst":"45.150.32.235","gateway":"192.168.31.1","dev":"wlp3s0","flags":[]}`)
		}, "45.150.32.235/32"},
		{"server /32 on another interface and metric", func(f *fakeIP) {
			f.routes4 = plus(cleanRoutes4, `{"dst":"45.150.32.235","dev":"wg0","metric":100,"flags":[]}`)
		}, "45.150.32.235/32 dev wg0"},
		{"lower split half", func(f *fakeIP) {
			f.routes4 = plus(cleanRoutes4, `{"dst":"0.0.0.0/1","dev":"wg0","scope":"253","flags":[]}`)
		}, "0.0.0.0/1"},
		{"upper split half", func(f *fakeIP) {
			f.routes4 = plus(cleanRoutes4, `{"dst":"128.0.0.0/1","dev":"tun0","scope":"253","flags":[]}`)
		}, "128.0.0.0/1"},
		{"direct table not empty", func(f *fakeIP) {
			f.routes4 = plus(cleanRoutes4, `{"dst":"10.9.0.0/16","dev":"wg0","table":"8888","flags":[]}`)
		}, "таблица 8888"},
		{"another rule for the direct mark", func(f *fakeIP) {
			f.rules = plus(cleanRules, `{"priority":100,"src":"all","fwmark":"0xff","table":"100"}`)
		}, "fwmark 0xff"},
		{"masked rule catching the direct mark", func(f *fakeIP) {
			f.rules = plus(cleanRules, `{"priority":100,"src":"all","fwmark":"0x1","fwmask":"0x1","table":"100"}`)
		}, "fwmark 0x1/0x1"},
		{"rule into the direct table", func(f *fakeIP) {
			f.rules = plus(cleanRules, `{"priority":200,"src":"10.9.0.0","srclen":16,"table":"8888"}`)
		}, "lookup 8888"},
		{"ipv6 half", func(f *fakeIP) {
			f.routes6 = plus(cleanRoutes6, `{"type":"7","dst":"::/1","dev":"lo","metric":1024,"flags":[]}`)
		}, "::/1"},
		{"tun carrying a foreign address", func(f *fakeIP) {
			f.addrs = `[{"ifindex":7,"ifname":"xray-tun","addr_info":[{"family":"inet","local":"10.8.0.2","prefixlen":24}]}]`
		}, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIPv6Stack(t, true)
			f := &fakeIP{routeGetOut: wanRouteGet}
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
		{"garbled output", func(f *fakeIP) { f.routes4 = "Dump terminated" }},
		{"unknown destination", func(f *fakeIP) { f.routes4 = `[{"dst":"somewhere","dev":"wg0"}]` }},
		{"unknown fwmark", func(f *fakeIP) { f.rules = `[{"priority":100,"fwmark":"mark","table":"100"}]` }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withIPv6Stack(t, true)
			f := &fakeIP{routeGetOut: wanRouteGet}
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
// what EnableTunRouting creates: they neither block it nor get touched.
func TestEnableTunRouting_LeavesUnrelatedEntriesAlone(t *testing.T) {
	withIPv6Stack(t, true)
	f := &fakeIP{
		routeGetOut: wanRouteGet,
		routes4: plus(cleanRoutes4,
			`{"dst":"45.150.32.0/24","gateway":"10.9.0.1","dev":"wg0","flags":[]}`,
			`{"dst":"45.150.32.235","dev":"wg0","table":"100","flags":[]}`,
			`{"dst":"198.51.100.9","dev":"wg0","flags":[]}`),
		routes6: plus(cleanRoutes6,
			`{"dst":"2000::/3","dev":"wg0","metric":1024,"flags":[]}`,
			`{"dst":"::/1","dev":"wg0","table":"8888","metric":1024,"flags":[]}`),
		rules: plus(cleanRules, `{"priority":100,"src":"all","fwmark":"0x100","table":"100"}`),
	}
	withFakeIP(t, f)

	if err := EnableTunRouting(tunCfg6); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "wg0") || strings.Contains(c, "table 100") {
			t.Errorf("touched a foreign entry: %q", c)
		}
	}
}
