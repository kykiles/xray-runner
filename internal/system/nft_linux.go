//go:build linux

package system

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Everything the program puts into netfilter lives in one table of its own,
// `inet xray-runner`: the kill switch's chain and the split's two. The table is
// the program's the way the tun routes are, so taking a feature down is
// deleting its chains, and taking the last one down is deleting the table —
// nothing in anybody else's table is touched, and a crashed run's leftovers are
// one name to remove (H09).
//
// It is spoken to over nfnetlink, in-process: no nft or iptables binary is run,
// so nothing has to inherit CAP_NET_ADMIN (H07), and a change goes in as one
// batch the kernel applies whole or not at all.
//
// There is no iptables fallback. nf_tables is in every kernel since 3.13, and
// every distribution this runs on ships it, with iptables itself a front end to
// it (iptables-nft). A kernel built without it gets a plain error, and a session
// that asked for the kill switch does not start. Two backends would double the
// rules, their tests and the ways they disagree, for no machine anyone uses.
const nftTable = "xray-runner"

// nftVerdict is what a rule does once it matches.
type nftVerdict int

const (
	nftAccept nftVerdict = iota
	nftDrop
	nftReturn
	nftRedirect   // to port, on this host
	nftRejectICMP // with code: ICMP for IPv4, ICMPv6 for IPv6
	nftRejectTCP  // with a reset
)

// nftRule is one rule as a list of matches, all of which must hold, and a
// verdict. Zero fields match anything.
type nftRule struct {
	// cgroup, when set, matches sockets of the cgroup at this path below the
	// cgroup v2 root, level components deep (nft's socket cgroupv2).
	cgroup  string
	level   int
	nfproto int // 4 or 6
	oif     string
	mark    uint32
	daddr   netip.Prefix
	l4proto uint8
	dport   uint16
	// icmpType matches the ICMPv6 type, when l4proto is ICMPv6.
	icmpType    uint8
	hasICMPType bool

	verdict nftVerdict
	port    uint16 // nftRedirect
	code    uint8  // nftRejectICMP
}

// nftChain is a base chain on the output hook.
type nftChain struct {
	name     string
	nat      bool
	priority int
	rules    []nftRule
}

// nftBackend is the table as the program reads and changes it. The real one is
// nfnetlink; tests model it.
type nftBackend interface {
	// chains lists the chains in our table, and nil when there is none.
	chains() ([]string, error)
	// apply runs one batch: the table deleted, or the named chains deleted, and
	// then the chains added in a table that is created if need be.
	apply(delTable bool, del []string, add []nftChain) error
}

// nftOps is overridable in tests.
var nftOps nftBackend = nfnetlink{}

// nftReplace puts chains in place of whatever chains of theirs names are in
// the table, in one batch.
func nftReplace(names []string, chains []nftChain) error {
	have, err := nftOps.chains()
	if err != nil {
		return nftError(err)
	}
	var del []string
	for _, n := range names {
		if slices.Contains(have, n) {
			del = append(del, n)
		}
	}
	return nftError(nftOps.apply(false, del, chains))
}

// nftRemove takes the named chains out, and the table with them once nothing
// else of ours is in it. It fails when one of them is still there afterwards:
// a kill switch that would not come out leaves the machine offline (G08).
// Nothing there to begin with is success.
func nftRemove(names []string) error {
	have, err := nftOps.chains()
	if err != nil {
		return nftError(err)
	}
	if have == nil {
		return nil
	}
	rest := slices.DeleteFunc(slices.Clone(have), func(c string) bool { return slices.Contains(names, c) })
	if len(rest) == 0 {
		err = nftOps.apply(true, nil, nil)
	} else if len(rest) < len(have) {
		var del []string
		for _, n := range names {
			if slices.Contains(have, n) {
				del = append(del, n)
			}
		}
		err = nftOps.apply(false, del, nil)
	}
	left, lerr := nftOps.chains()
	if lerr != nil {
		return nftError(errors.Join(err, lerr))
	}
	var still []string
	for _, n := range names {
		if slices.Contains(left, n) {
			still = append(still, n)
		}
	}
	if len(still) > 0 {
		return fmt.Errorf("цепочки %v таблицы inet %s остались на месте: %w", still, nftTable, nftError(err))
	}
	return nil
}

// nftDropTable removes our table whole, for the recovery after a crash.
func nftDropTable() error {
	have, err := nftOps.chains()
	if err != nil || have == nil {
		return nftError(err)
	}
	return nftError(nftOps.apply(true, nil, nil))
}

// nftError says what a bare errno from nfnetlink means for the user.
func nftError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EPERM):
		return fmt.Errorf("nftables: нет прав (нужен root или CAP_NET_ADMIN): %w", err)
	case errors.Is(err, syscall.EPROTONOSUPPORT), errors.Is(err, syscall.EAFNOSUPPORT):
		return fmt.Errorf("nftables недоступен в ядре: %w", err)
	case errors.Is(err, syscall.EOPNOTSUPP):
		return fmt.Errorf("ядро не поддерживает нужное выражение nftables (для раздельной маршрутизации нужно ядро 5.2 или новее): %w", err)
	}
	return fmt.Errorf("nftables: %w", err)
}

// --- nfnetlink -----------------------------------------------------------------

type nfnetlink struct{}

var ourTable = &nftables.Table{Name: nftTable, Family: nftables.TableFamilyINet}

func (nfnetlink) chains() ([]string, error) {
	c, err := nftables.New()
	if err != nil {
		return nil, err
	}
	tables, err := c.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(tables, func(t *nftables.Table) bool { return t.Name == nftTable }) {
		return nil, nil
	}
	chains, err := c.ListChainsOfTableFamily(nftables.TableFamilyINet)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, ch := range chains {
		if ch.Table != nil && ch.Table.Name == nftTable {
			out = append(out, ch.Name)
		}
	}
	return out, nil
}

func (nfnetlink) apply(delTable bool, del []string, add []nftChain) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	if delTable {
		c.DelTable(ourTable)
	}
	for _, name := range del {
		c.DelChain(&nftables.Chain{Name: name, Table: ourTable})
	}
	if len(add) > 0 {
		c.AddTable(ourTable)
	}
	for _, ch := range add {
		typ, prio := nftables.ChainTypeFilter, nftables.ChainPriority(ch.priority)
		if ch.nat {
			typ = nftables.ChainTypeNAT
		}
		policy := nftables.ChainPolicyAccept
		chain := c.AddChain(&nftables.Chain{
			Name:     ch.name,
			Table:    ourTable,
			Type:     typ,
			Hooknum:  nftables.ChainHookOutput,
			Priority: &prio,
			Policy:   &policy,
		})
		for _, r := range ch.rules {
			exprs, err := r.exprs()
			if err != nil {
				return err
			}
			c.AddRule(&nftables.Rule{Table: ourTable, Chain: chain, Exprs: exprs})
		}
	}
	return c.Flush()
}

// exprs compiles the rule the way nft compiles its text form.
func (r nftRule) exprs() ([]expr.Any, error) {
	var out []expr.Any
	cmp := func(data []byte) {
		out = append(out, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data})
	}
	meta := func(key expr.MetaKey) {
		out = append(out, &expr.Meta{Key: key, Register: 1})
	}

	if r.cgroup != "" {
		// nft names the cgroup by path and loads its inode number, which is
		// the cgroup v2 id, in host order.
		var st unix.Stat_t
		if err := unix.Stat(filepath.Join(cgroupRoot, r.cgroup), &st); err != nil {
			return nil, fmt.Errorf("cgroup %s: %w", r.cgroup, err)
		}
		out = append(out, &expr.Socket{Key: expr.SocketKeyCgroupv2, Level: uint32(r.level), Register: 1})
		cmp(binary.NativeEndian.AppendUint64(nil, st.Ino))
	}
	switch r.nfproto {
	case 4:
		meta(expr.MetaKeyNFPROTO)
		cmp([]byte{unix.NFPROTO_IPV4})
	case 6:
		meta(expr.MetaKeyNFPROTO)
		cmp([]byte{unix.NFPROTO_IPV6})
	}
	if r.oif != "" {
		meta(expr.MetaKeyOIFNAME)
		name := make([]byte, unix.IFNAMSIZ)
		copy(name, r.oif)
		cmp(name)
	}
	if r.mark != 0 {
		meta(expr.MetaKeyMARK)
		cmp(binary.NativeEndian.AppendUint32(nil, r.mark))
	}
	if r.daddr.IsValid() {
		addr := r.daddr.Addr()
		offset, size := uint32(16), uint32(4) // ip daddr
		if addr.Is6() {
			offset, size = 24, 16 // ip6 daddr
		}
		out = append(out, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: size})
		if r.daddr.Bits() < addr.BitLen() {
			out = append(out, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: size, Mask: maskBytes(r.daddr.Bits(), int(size)), Xor: make([]byte, size)})
			addr = r.daddr.Masked().Addr()
		}
		cmp(addr.AsSlice())
	}
	if r.l4proto != 0 {
		meta(expr.MetaKeyL4PROTO)
		cmp([]byte{r.l4proto})
	}
	if r.dport != 0 {
		out = append(out, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2})
		cmp(binary.BigEndian.AppendUint16(nil, r.dport))
	}
	if r.hasICMPType {
		out = append(out, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1})
		cmp([]byte{r.icmpType})
	}

	switch r.verdict {
	case nftAccept:
		out = append(out, &expr.Verdict{Kind: expr.VerdictAccept})
	case nftDrop:
		out = append(out, &expr.Verdict{Kind: expr.VerdictDrop})
	case nftReturn:
		out = append(out, &expr.Verdict{Kind: expr.VerdictReturn})
	case nftRedirect:
		out = append(out,
			&expr.Immediate{Register: 1, Data: binary.BigEndian.AppendUint16(nil, r.port)},
			&expr.Redir{RegisterProtoMin: 1})
	case nftRejectICMP:
		out = append(out, &expr.Reject{Type: unix.NFT_REJECT_ICMP_UNREACH, Code: r.code})
	case nftRejectTCP:
		out = append(out, &expr.Reject{Type: unix.NFT_REJECT_TCP_RST})
	default:
		return nil, fmt.Errorf("неизвестный вердикт %d", r.verdict)
	}
	return out, nil
}

func maskBytes(bits, size int) []byte {
	m := make([]byte, size)
	for i := range m {
		switch {
		case bits >= 8:
			m[i] = 0xff
			bits -= 8
		case bits > 0:
			m[i] = byte(0xff << (8 - bits))
			bits = 0
		}
	}
	return m
}

// legacySplitTable is where the split's rules lived before H09: a table of
// that name in each of the ip and ip6 families.
const legacySplitTable = "xray_split"

// dropLegacySplit removes those tables, for the recovery of a record a version
// before H09 wrote; a session of this version never makes them. Overridable in
// tests.
var dropLegacySplit = func() error {
	c, err := nftables.New()
	if err != nil {
		return nftError(err)
	}
	found := false
	for _, fam := range []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6} {
		tables, err := c.ListTablesOfFamily(fam)
		if err != nil {
			return nftError(err)
		}
		for _, t := range tables {
			if t.Name == legacySplitTable {
				c.DelTable(t)
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return nftError(c.Flush())
}
