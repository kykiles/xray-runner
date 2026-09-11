//go:build linux

package system

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"xray-runner/internal/xraycfg"
)

// fakeIPTables is a minimal stateful model of iptables/ip6tables, enough to
// verify the kill-switch logic (chain creation, rule ordering, OUTPUT jumps)
// without root or a real binary.
type fakeChain struct {
	exists bool
	rules  []string
}

type fakeIPTables struct {
	available map[string]bool
	chains    map[string]*fakeChain // key: bin
	jumps     map[string]int        // key: bin -> OUTPUT jump count
	// output is the OUTPUT chain in order. Position decides whether the kill
	// switch is reached at all, so a plain count can't model it: an ACCEPT in
	// an earlier rule ends traversal before ours runs.
	output map[string][]string
	// failNew makes -N fail for these binaries, the way a stack without
	// the needed kernel module refuses to create the chain.
	failNew map[string]bool
}

func newFakeIPTables(bins ...string) *fakeIPTables {
	f := &fakeIPTables{
		available: map[string]bool{},
		chains:    map[string]*fakeChain{},
		jumps:     map[string]int{},
		output:    map[string][]string{},
	}
	for _, b := range bins {
		f.available[b] = true
		f.chains[b] = &fakeChain{}
	}
	return f
}

func (f *fakeIPTables) lookPath(bin string) error {
	if f.available[bin] {
		return nil
	}
	return fmt.Errorf("exec: %q not found", bin)
}

func (f *fakeIPTables) run(bin string, args ...string) ([]byte, error) {
	c := f.chains[bin]
	if c == nil {
		c = &fakeChain{}
		f.chains[bin] = c
	}
	switch {
	case len(args) == 2 && args[0] == "-N":
		if f.failNew[bin] {
			return []byte("can't initialize ip6tables table `filter'"), fmt.Errorf("exit status 3")
		}
		if c.exists {
			return []byte("iptables: Chain already exists."), fmt.Errorf("exit status 1")
		}
		c.exists = true
	case args[0] == "-A" && args[1] == "OUTPUT":
		f.jumps[bin]++
		f.output[bin] = append(f.output[bin], strings.Join(args, " "))
	case args[0] == "-I" && args[1] == "OUTPUT":
		// -I OUTPUT [pos] -j CHAIN; iptables defaults to position 1.
		pos := 1
		if len(args) > 2 {
			if n, err := strconv.Atoi(args[2]); err == nil {
				pos = n
			}
		}
		if pos < 1 {
			pos = 1
		}
		if pos > len(f.output[bin])+1 {
			pos = len(f.output[bin]) + 1
		}
		rule := strings.Join(args, " ")
		f.output[bin] = append(f.output[bin][:pos-1],
			append([]string{rule}, f.output[bin][pos-1:]...)...)
		f.jumps[bin]++
	case args[0] == "-A" && args[1] == killSwitchChain:
		c.rules = append(c.rules, fmt.Sprintf("%v", args))
	case args[0] == "-D" && args[1] == "OUTPUT":
		if f.jumps[bin] <= 0 {
			return nil, fmt.Errorf("exit status 1") // nothing to delete
		}
		f.jumps[bin]--
		if i := ruleIndex(f.output[bin], killSwitchChain); i >= 0 {
			f.output[bin] = append(f.output[bin][:i], f.output[bin][i+1:]...)
		}
	case args[0] == "-F":
		c.rules = nil
	case args[0] == "-X":
		c.exists = false
	}
	return nil, nil
}

func withFakeFirewall(t *testing.T, f *fakeIPTables) {
	t.Helper()
	orig := fwCmd
	fwCmd = f
	t.Cleanup(func() { fwCmd = orig })
}

// ipv4Cfg is a representative kill-switch config with an IPv4 server endpoint.
var ipv4Cfg = KillSwitchConfig{Endpoints: []Endpoint{{IP: "203.0.113.5", Port: 443}}}

// chainHasServerAccept reports whether the chain contains an ACCEPT rule for
// the given server IP.
func chainHasServerAccept(rules []string, ip string) bool {
	for _, r := range rules {
		if strings.Contains(r, "-d "+ip) && strings.Contains(r, "ACCEPT") {
			return true
		}
	}
	return false
}

// A02: a failed enable is reported as "no kill switch", so teardown never
// removes it. Whatever went in before the failure has to come out right there —
// a working IPv4 block left behind would cut the network after exit.
func TestEnableKillSwitchRollsBackOnFailure(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	f.failNew = map[string]bool{"ip6tables": true}
	withFakeFirewall(t, f)

	if err := EnableKillSwitch(ipv4Cfg); err == nil {
		t.Fatal("EnableKillSwitch succeeded with ip6tables failing")
	}
	for _, bin := range []string{"iptables", "ip6tables"} {
		if f.jumps[bin] != 0 || f.chains[bin].exists {
			t.Errorf("%s: jumps=%d chain=%v left behind after a failed enable",
				bin, f.jumps[bin], f.chains[bin].exists)
		}
	}
}

func TestEnableKillSwitchAppliesBothStacks(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	withFakeFirewall(t, f)

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	for _, bin := range []string{"iptables", "ip6tables"} {
		if got := f.jumps[bin]; got != 1 {
			t.Errorf("%s: OUTPUT jumps = %d, want 1", bin, got)
		}
	}
	// iptables (IPv4 stack) gets the server ACCEPT rule; ip6tables doesn't
	// (IPv4 -d would be invalid there), so it has one rule fewer.
	if got := len(f.chains["iptables"].rules); got != 8 {
		t.Errorf("iptables: chain rules = %d, want 8", got)
	}
	if got := len(f.chains["ip6tables"].rules); got != 7 {
		t.Errorf("ip6tables: chain rules = %d, want 7", got)
	}
	if !chainHasServerAccept(f.chains["iptables"].rules, ipv4Cfg.Endpoints[0].IP) {
		t.Errorf("iptables: missing ACCEPT rule for server %s", ipv4Cfg.Endpoints[0].IP)
	}
	if chainHasServerAccept(f.chains["ip6tables"].rules, ipv4Cfg.Endpoints[0].IP) {
		t.Errorf("ip6tables: unexpected IPv4 server ACCEPT rule")
	}
}

// ruleIndex finds the position of the first rule containing every fragment,
// or -1. Order matters in iptables, so the tests below assert on indices.
func ruleIndex(rules []string, fragments ...string) int {
	for i, r := range rules {
		found := true
		for _, f := range fragments {
			if !strings.Contains(r, f) {
				found = false
				break
			}
		}
		if found {
			return i
		}
	}
	return -1
}

// Traffic the panel routes `direct` leaves through the physical interface
// carrying xraycfg.DirectFwMark, so without an explicit ACCEPT it reaches the
// chain's final DROP: not a leak, but every direct destination goes dark while
// the kill switch is on.
func TestEnableKillSwitchAcceptsMarkedDirectTraffic(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	withFakeFirewall(t, f)

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	mark := strconv.Itoa(xraycfg.DirectFwMark)
	for _, bin := range []string{"iptables", "ip6tables"} {
		rules := f.chains[bin].rules
		accept := ruleIndex(rules, "--mark "+mark, "ACCEPT")
		if accept < 0 {
			t.Fatalf("%s: no ACCEPT rule for fwmark %s in %v", bin, mark, rules)
		}
		drop := ruleIndex(rules, "-j DROP")
		if drop < 0 {
			t.Fatalf("%s: no DROP rule in %v", bin, rules)
		}
		if accept > drop {
			t.Errorf("%s: fwmark ACCEPT at %d comes after DROP at %d", bin, accept, drop)
		}
	}
}

// ufw (active on plenty of desktops) installs its own OUTPUT jumps, and an
// ACCEPT inside one of them ends traversal of OUTPUT. A kill switch appended
// after them is never reached, so it must go in at the front instead.
func TestEnableKillSwitchJumpsBeforeExistingOutputRules(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	for _, bin := range []string{"iptables", "ip6tables"} {
		f.output[bin] = []string{
			"-A OUTPUT -j ufw-before-logging-output",
			"-A OUTPUT -j ufw-user-output",
		}
		f.jumps[bin] = 2
	}
	withFakeFirewall(t, f)

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	for _, bin := range []string{"iptables", "ip6tables"} {
		at := ruleIndex(f.output[bin], killSwitchChain)
		if at != 0 {
			t.Errorf("%s: kill switch jump at position %d, want 0 (OUTPUT: %v)",
				bin, at, f.output[bin])
		}
	}
}

func TestEnableKillSwitchIsIdempotent(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	withFakeFirewall(t, f)

	for i := 0; i < 3; i++ {
		if err := EnableKillSwitch(ipv4Cfg); err != nil {
			t.Fatalf("EnableKillSwitch run %d: %v", i, err)
		}
	}

	// No matter how many times we enable, exactly one OUTPUT jump and one set
	// of chain rules must remain — no accumulation.
	for _, bin := range []string{"iptables", "ip6tables"} {
		if got := f.jumps[bin]; got != 1 {
			t.Errorf("%s: OUTPUT jumps after 3 enables = %d, want 1", bin, got)
		}
	}
	if got := len(f.chains["iptables"].rules); got != 8 {
		t.Errorf("iptables: chain rules after 3 enables = %d, want 8", got)
	}
	if got := len(f.chains["ip6tables"].rules); got != 7 {
		t.Errorf("ip6tables: chain rules after 3 enables = %d, want 7", got)
	}
}

func TestEnableKillSwitchSkipsMissingBinary(t *testing.T) {
	f := newFakeIPTables("iptables") // ip6tables unavailable
	withFakeFirewall(t, f)

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	if f.jumps["iptables"] != 1 {
		t.Errorf("iptables OUTPUT jumps = %d, want 1", f.jumps["iptables"])
	}
	if f.jumps["ip6tables"] != 0 {
		t.Errorf("ip6tables should be skipped, but got %d jumps", f.jumps["ip6tables"])
	}
}

func TestDisableKillSwitchRemovesAllJumps(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	withFakeFirewall(t, f)

	// Simulate an older buggy state: several accumulated OUTPUT jumps.
	f.jumps["iptables"] = 3
	f.jumps["ip6tables"] = 2

	if err := DisableKillSwitch(); err != nil {
		t.Fatalf("DisableKillSwitch: %v", err)
	}

	for _, bin := range []string{"iptables", "ip6tables"} {
		if f.jumps[bin] != 0 {
			t.Errorf("%s: OUTPUT jumps after disable = %d, want 0", bin, f.jumps[bin])
		}
		if f.chains[bin].exists {
			t.Errorf("%s: chain should be removed", bin)
		}
	}
}

// A server picked out of a multi-server profile still runs under that profile's
// routing, and a panel rule may send some domains through a sibling outbound.
// Whitelisting only the chosen endpoint sends xray's uplink to the siblings
// straight into the DROP — a partial blackhole with no diagnostic.
func TestEnableKillSwitchAcceptsEveryEndpointBeforeDropping(t *testing.T) {
	f := newFakeIPTables("iptables", "ip6tables")
	withFakeFirewall(t, f)

	cfg := KillSwitchConfig{Endpoints: []Endpoint{
		{IP: "203.0.113.5", Port: 443},
		{IP: "203.0.113.6", Port: 8443, UDP: true},
	}}
	if err := EnableKillSwitch(cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	rules := f.chains["iptables"].rules
	drop := ruleIndex(rules, "-j DROP")
	if drop < 0 {
		t.Fatalf("no DROP rule: %v", rules)
	}
	for _, e := range cfg.Endpoints {
		idx := ruleIndex(rules, "-d "+e.IP, "ACCEPT")
		if idx < 0 {
			t.Errorf("missing ACCEPT for endpoint %s: %v", e.IP, rules)
			continue
		}
		if idx > drop {
			t.Errorf("ACCEPT for %s comes after DROP (%d > %d)", e.IP, idx, drop)
		}
	}
	if idx := ruleIndex(rules, "-d 203.0.113.6", "-p udp", "--dport 8443"); idx < 0 {
		t.Errorf("hysteria2 sibling did not get a udp rule: %v", rules)
	}
}
