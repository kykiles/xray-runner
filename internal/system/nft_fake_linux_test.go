//go:build linux

package system

import (
	"errors"
	"net/netip"
	"slices"
	"testing"
)

// fakeNft models our nftables table: which chains it holds and their rules.
// A batch applies whole or not at all, as the kernel's does.
type fakeNft struct {
	table map[string]nftChain // nil: no table
	// failApply fails every batch; stuck keeps chains a delete names, the way
	// a delete that went wrong leaves them.
	failApply error
	failList  error
	stuck     bool
	batches   int
}

func (f *fakeNft) chains() ([]string, error) {
	if f.failList != nil {
		return nil, f.failList
	}
	if f.table == nil {
		return nil, nil
	}
	out := []string{}
	for name := range f.table {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeNft) apply(delTable bool, del []string, add []nftChain) error {
	f.batches++
	if f.failApply != nil {
		return f.failApply
	}
	next := map[string]nftChain{}
	for k, v := range f.table {
		next[k] = v
	}
	exists := f.table != nil
	if delTable {
		if !exists {
			return errors.New("no such file or directory")
		}
		if !f.stuck {
			next, exists = map[string]nftChain{}, false
		}
	}
	for _, name := range del {
		if _, ok := next[name]; !ok {
			return errors.New("no such file or directory")
		}
		if !f.stuck {
			delete(next, name)
		}
	}
	for _, c := range add {
		if _, ok := next[c.name]; ok {
			return errors.New("file exists")
		}
		next[c.name] = c
		exists = true
	}
	if exists {
		f.table = next
	} else {
		f.table = nil
	}
	return nil
}

// withFakeNft swaps in the model, and keeps the recovery's cleanup of what a
// version before H09 left off the host's own iptables and nftables.
func withFakeNft(t *testing.T) *fakeNft {
	t.Helper()
	f := &fakeNft{}
	origOps, origLegacy, origDrop := nftOps, legacyCmd, dropLegacySplit
	nftOps, legacyCmd, dropLegacySplit = f, &fakeIPTables{}, func() error { return nil }
	t.Cleanup(func() { nftOps, legacyCmd, dropLegacySplit = origOps, origLegacy, origDrop })
	return f
}

// packet is one outgoing packet as the output hook sees it, for nftEval.
type packet struct {
	cgroup  string // the socket's cgroup, below the hierarchy root
	oif     string
	mark    uint32
	daddr   string
	l4proto uint8
	dport   uint16
	icmp    uint8
}

func (p packet) v6() bool { return netip.MustParseAddr(p.daddr).Is6() }

// nftEval runs a packet through a chain: the first rule whose matches all hold
// decides, and falling off the end is the chain's accept policy.
func nftEval(c nftChain, p packet) (nftRule, bool) {
	for _, r := range c.rules {
		if r.matches(p) {
			return r, true
		}
	}
	return nftRule{verdict: nftAccept}, false
}

func (r nftRule) matches(p packet) bool {
	switch {
	case r.cgroup != "" && r.cgroup != p.cgroup,
		r.nfproto == 4 && p.v6(),
		r.nfproto == 6 && !p.v6(),
		r.oif != "" && r.oif != p.oif,
		r.mark != 0 && r.mark != p.mark,
		r.daddr.IsValid() && !r.daddr.Contains(netip.MustParseAddr(p.daddr)),
		r.l4proto != 0 && r.l4proto != p.l4proto,
		r.dport != 0 && r.dport != p.dport,
		r.hasICMPType && r.icmpType != p.icmp:
		return false
	}
	return true
}
