//go:build linux

package system

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"

	"xray-runner/internal/xraycfg"
)

// ipv4Cfg is a representative kill-switch config with an IPv4 server endpoint.
var ipv4Cfg = KillSwitchConfig{Endpoints: []Endpoint{{IP: "203.0.113.5", Port: 443}}}

func killSwitchIn(t *testing.T, f *fakeNft) nftChain {
	t.Helper()
	c, ok := f.table[killSwitchChain]
	if !ok {
		t.Fatalf("no kill switch chain in the table: %v", f.table)
	}
	return c
}

// What passes and what does not, for both families in the one inet chain
// (A02): the tunnel, loopback, the core's marked direct traffic and its
// servers, DNS and IPv6's own housekeeping — and nothing else.
func TestKillSwitchDecisions(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	cfg := KillSwitchConfig{Endpoints: []Endpoint{
		{IP: "203.0.113.5", Port: 443},
		{IP: "2001:db8::5", Port: 8443, UDP: true},
		{IP: "::ffff:198.51.100.9", Port: 80},
	}}
	if err := EnableKillSwitch(cfg); err != nil {
		t.Fatal(err)
	}
	c := killSwitchIn(t, f)
	cases := []struct {
		name string
		p    packet
		want bool
	}{
		{"app on the physical path", packet{oif: "eth0", daddr: "1.1.1.1", l4proto: protoTCP, dport: 443}, false},
		{"app over IPv6", packet{oif: "eth0", daddr: "2606:4700::1111", l4proto: protoTCP, dport: 443}, false},
		{"QUIC", packet{oif: "eth0", daddr: "1.1.1.1", l4proto: protoUDP, dport: 443}, false},
		{"through the tunnel", packet{oif: "xray-tun", daddr: "1.1.1.1", l4proto: protoTCP, dport: 443}, true},
		{"loopback", packet{oif: "lo", daddr: "127.0.0.1", l4proto: protoTCP, dport: 10808}, true},
		{"marked direct", packet{oif: "eth0", mark: xraycfg.DirectFwMark, daddr: "93.184.216.34", l4proto: protoTCP, dport: 443}, true},
		{"another mark", packet{oif: "eth0", mark: 1, daddr: "93.184.216.34", l4proto: protoTCP, dport: 443}, false},
		{"server", packet{oif: "eth0", daddr: "203.0.113.5", l4proto: protoTCP, dport: 443}, true},
		{"server, other port", packet{oif: "eth0", daddr: "203.0.113.5", l4proto: protoTCP, dport: 22}, false},
		{"server, other protocol", packet{oif: "eth0", daddr: "203.0.113.5", l4proto: protoUDP, dport: 443}, false},
		{"IPv6 server over UDP", packet{oif: "eth0", daddr: "2001:db8::5", l4proto: protoUDP, dport: 8443}, true},
		{"mapped server as IPv4", packet{oif: "eth0", daddr: "198.51.100.9", l4proto: protoTCP, dport: 80}, true},
		{"DNS", packet{oif: "eth0", daddr: "8.8.8.8", l4proto: protoUDP, dport: 53}, true},
		{"DNS over TCP, IPv6", packet{oif: "eth0", daddr: "2001:4860:4860::8888", l4proto: protoTCP, dport: 53}, true},
		{"DHCPv6", packet{oif: "eth0", daddr: "ff02::1:2", l4proto: protoUDP, dport: 547}, true},
		{"neighbour solicitation", packet{oif: "eth0", daddr: "ff02::1:ff00:1", l4proto: protoICMPv6, icmp: 135}, true},
		{"ICMPv6 echo", packet{oif: "eth0", daddr: "2606:4700::1111", l4proto: protoICMPv6, icmp: 128}, false},
	}
	for _, tc := range cases {
		r, matched := nftEval(c, tc.p)
		if !matched {
			t.Errorf("%s: fell off the end of the chain", tc.name)
			continue
		}
		if got := r.verdict == nftAccept; got != tc.want {
			t.Errorf("%s: accepted = %v, want %v (rule %+v)", tc.name, got, tc.want, r)
		}
	}
}

// The chain is ours, in our table, and its last word is a drop.
func TestKillSwitchChainShape(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatal(err)
	}
	c := killSwitchIn(t, f)
	if c.nat || c.priority != 0 {
		t.Errorf("chain %+v: want a filter chain at priority 0", c)
	}
	if last := c.rules[len(c.rules)-1]; last.verdict != nftDrop || last.matches(packet{daddr: "1.1.1.1"}) != true {
		t.Errorf("last rule %+v, want an unconditional drop", last)
	}
	for _, r := range c.rules[:len(c.rules)-1] {
		if r.verdict != nftAccept {
			t.Errorf("rule %+v before the drop is not an accept", r)
		}
		if r == (nftRule{verdict: nftAccept}) {
			t.Errorf("an unconditional accept opens everything")
		}
	}
}

// A02: a kill switch that did not go in is an error, and a refused batch leaves
// nothing behind — there is no half-built switch.
func TestEnableKillSwitchFailureLeavesNothing(t *testing.T) {
	f := withFakeNft(t)
	j := withJournal(t)
	f.failApply = syscall.EPROTONOSUPPORT
	err := EnableKillSwitch(ipv4Cfg)
	if err == nil {
		t.Fatal("EnableKillSwitch succeeded with nftables refusing the batch")
	}
	if !strings.Contains(err.Error(), "nftables недоступен") {
		t.Errorf("error %q does not say nftables is missing", err)
	}
	if f.table != nil {
		t.Errorf("a refused batch left %v", f.table)
	}
	if len(j.m) != 0 {
		t.Errorf("the journal still names a kill switch that never went in: %v", j.m)
	}
}

func TestEnableKillSwitchIsIdempotent(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	for range 3 {
		if err := EnableKillSwitch(ipv4Cfg); err != nil {
			t.Fatal(err)
		}
	}
	chains, _ := f.chains()
	if !slices.Equal(chains, []string{killSwitchChain}) {
		t.Errorf("chains after three enables: %v", chains)
	}
	first := len(killSwitchIn(t, f).rules)
	if want, _ := killSwitchRuleset(ipv4Cfg); first != len(want.rules) {
		t.Errorf("chain has %d rules, want %d", first, len(want.rules))
	}
}

func TestEnableKillSwitchRefusesABadEndpoint(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	for _, e := range []Endpoint{{IP: "", Port: 443}, {IP: "example.com", Port: 443}, {IP: "1.2.3.4", Port: 0}, {IP: "fe80::1%eth0", Port: 443}} {
		if err := EnableKillSwitch(KillSwitchConfig{Endpoints: []Endpoint{e}}); err == nil {
			t.Errorf("endpoint %+v accepted", e)
		}
	}
	if f.batches != 0 {
		t.Errorf("%d batches sent for endpoints that were refused", f.batches)
	}
}

// The table goes with the last chain in it, and stays while the split still
// has chains there.
func TestDisableKillSwitchTakesTheTableWhenAlone(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatal(err)
	}
	if err := DisableKillSwitch(); err != nil {
		t.Fatal(err)
	}
	if f.table != nil {
		t.Errorf("table left behind: %v", f.table)
	}

	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatal(err)
	}
	f.table[splitNatChain] = nftChain{name: splitNatChain}
	if err := DisableKillSwitch(); err != nil {
		t.Fatal(err)
	}
	chains, _ := f.chains()
	if !slices.Equal(chains, []string{splitNatChain}) {
		t.Errorf("chains after the kill switch went: %v, want the split's left", chains)
	}
}

// G08: a kill switch that would not come out leaves the machine offline, and
// the session has to hear it.
func TestDisableKillSwitchReportsAChainLeftBehind(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	if err := EnableKillSwitch(ipv4Cfg); err != nil {
		t.Fatal(err)
	}
	f.stuck = true
	if err := DisableKillSwitch(); err == nil {
		t.Fatal("DisableKillSwitch succeeded with the chain still there")
	}
}

func TestDisableKillSwitchOnACleanHost(t *testing.T) {
	f := withFakeNft(t)
	withJournal(t)
	if err := DisableKillSwitch(); err != nil {
		t.Fatalf("DisableKillSwitch on a clean host: %v", err)
	}
	if f.batches != 0 {
		t.Errorf("%d batches sent with nothing to remove", f.batches)
	}
}

func TestNftErrorNamesTheCause(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{syscall.EPERM, "нет прав"},
		{fmt.Errorf("netlink: %w", syscall.EPROTONOSUPPORT), "недоступен в ядре"},
		{syscall.EOPNOTSUPP, "5.2"},
	} {
		if got := nftError(c.err); !strings.Contains(got.Error(), c.want) || !errors.Is(got, c.err) {
			t.Errorf("nftError(%v) = %v, want it to say %q and wrap the cause", c.err, got, c.want)
		}
	}
}

// fakeIPTables is the iptables of a host a version before H09 left its chain
// on, for the recovery's one-off cleanup.
type fakeIPTables struct {
	chain map[string]bool // bin → XRAY_KILL exists
	jumps map[string]int
	calls []string
}

func (f *fakeIPTables) lookPath(bin string) error {
	if _, ok := f.chain[bin]; ok {
		return nil
	}
	return errors.New("not found")
}

func (f *fakeIPTables) run(bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, bin+" "+strings.Join(args, " "))
	if len(args) == 0 || args[0] != "-w" {
		return nil, errors.New("run without -w")
	}
	switch strings.Join(args[1:], " ") {
	case "-S " + legacyChain:
		if !f.chain[bin] {
			return nil, errors.New("no chain")
		}
	case "-D OUTPUT -j " + legacyChain:
		if f.jumps[bin] == 0 {
			return nil, errors.New("no rule")
		}
		f.jumps[bin]--
	case "-F " + legacyChain:
	case "-X " + legacyChain:
		if f.jumps[bin] > 0 {
			return nil, errors.New("chain in use")
		}
		f.chain[bin] = false
	default:
		return nil, fmt.Errorf("unmodelled %v", args)
	}
	return nil, nil
}

// A record written before H09 names an iptables chain; the recovery takes it
// out too, every OUTPUT jump first.
func TestRecoverKillSwitchRemovesTheLegacyChain(t *testing.T) {
	withFakeNft(t)
	withJournal(t)
	f := &fakeIPTables{chain: map[string]bool{"iptables": true, "ip6tables": true}, jumps: map[string]int{"iptables": 2, "ip6tables": 1}}
	legacyCmd = f

	if err := recoverKillSwitch(); err != nil {
		t.Fatal(err)
	}
	if f.chain["iptables"] || f.chain["ip6tables"] {
		t.Errorf("legacy chain left: %v (calls %v)", f.chain, f.calls)
	}
}

func TestRecoverKillSwitchWithoutIptables(t *testing.T) {
	withFakeNft(t)
	withJournal(t)
	if err := recoverKillSwitch(); err != nil {
		t.Fatalf("recovery on a host without iptables: %v", err)
	}
}
