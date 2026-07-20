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
}

func newFakeIPTables(bins ...string) *fakeIPTables {
	f := &fakeIPTables{
		available: map[string]bool{},
		chains:    map[string]*fakeChain{},
		jumps:     map[string]int{},
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
		if c.exists {
			return []byte("iptables: Chain already exists."), fmt.Errorf("exit status 1")
		}
		c.exists = true
	case args[0] == "-A" && args[1] == "OUTPUT":
		f.jumps[bin]++
	case args[0] == "-A" && args[1] == killSwitchChain:
		c.rules = append(c.rules, fmt.Sprintf("%v", args))
	case args[0] == "-D" && args[1] == "OUTPUT":
		if f.jumps[bin] <= 0 {
			return nil, fmt.Errorf("exit status 1") // nothing to delete
		}
		f.jumps[bin]--
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
var ipv4Cfg = KillSwitchConfig{ServerIP: "203.0.113.5", ServerPort: 443}

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
	if !chainHasServerAccept(f.chains["iptables"].rules, ipv4Cfg.ServerIP) {
		t.Errorf("iptables: missing ACCEPT rule for server %s", ipv4Cfg.ServerIP)
	}
	if chainHasServerAccept(f.chains["ip6tables"].rules, ipv4Cfg.ServerIP) {
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
