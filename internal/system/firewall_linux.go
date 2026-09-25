//go:build linux

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"xray-runner/internal/xraycfg"
)

// killSwitchChain is the kill switch's chain in our nftables table (nft_linux.go).
const killSwitchChain = "killswitch"

func EnableKillSwitch(cfg KillSwitchConfig) error {
	slog.Info("enabling kill switch via nftables", "endpoints", len(cfg.Endpoints))
	chain, err := killSwitchRuleset(cfg)
	if err != nil {
		return err
	}
	// Written down before the batch: a run killed with the switch in place would
	// otherwise leave the network cut until a reboot (H06). The teardown needs
	// nothing but the table's name.
	note(func(j *journal) { j.KillSwitch = true })
	// One batch replaces any chain an earlier call left, so repeated runs never
	// stack a second set, and a batch the kernel refuses changes nothing: there
	// is no half-built switch to roll back.
	if err := nftReplace([]string{killSwitchChain}, []nftChain{chain}); err != nil {
		note(func(j *journal) { j.KillSwitch = false })
		return fmt.Errorf("kill switch не включился: %w", err)
	}
	return nil
}

// killSwitchRuleset is the kill switch: one filter chain on output that drops
// everything but what the rules before the drop let through. One inet chain
// covers IPv4 and IPv6 alike (A02), and it works the same whether the kernel
// has IPv6 or not.
//
// Its own chain at the filter priority is evaluated whatever other chains —
// ufw's, docker's, firewalld's — do with the packet, and an accept elsewhere
// does not carry past our drop: the jump at the front of OUTPUT the iptables
// version needed for that is not needed here.
//
// Nothing is accepted by conntrack state (A09): an established accept let every
// connection opened before the switch keep flowing past it on the physical
// path. What must pass does so by its own rule below.
func killSwitchRuleset(cfg KillSwitchConfig) (nftChain, error) {
	rules := []nftRule{
		{oif: "lo", verdict: nftAccept},
		{oif: "xray-tun", verdict: nftAccept},
		// Traffic the panel routes `direct` carries this mark and leaves through
		// the physical interface, so the xray-tun rule above never covers it.
		// This opens nothing when xray is down: the mark exists only on sockets
		// xray's freedom outbounds create.
		{mark: xraycfg.DirectFwMark, verdict: nftAccept},
	}
	// xray's own traffic to the VPN servers — every packet of it — or the uplink,
	// every reconnect and hysteria2's UDP flows are dropped and the tunnel can
	// never recover.
	for _, e := range cfg.Endpoints {
		ip, err := netip.ParseAddr(e.IP)
		if err != nil || ip.Zone() != "" || e.Port <= 0 || e.Port > 65535 {
			return nftChain{}, fmt.Errorf("kill switch: неверный адрес сервера %s:%d", e.IP, e.Port)
		}
		ip = ip.Unmap()
		r := nftRule{nfproto: 4, daddr: netip.PrefixFrom(ip, ip.BitLen()), l4proto: protoTCP, dport: uint16(e.Port), verdict: nftAccept}
		if ip.Is6() {
			r.nfproto = 6
		}
		if e.UDP {
			r.l4proto = protoUDP
		}
		rules = append(rules, r)
	}
	// DNS, so name resolution keeps working while the switch is up (resolving
	// the server on reconnect). In TUN mode DNS normally goes through the
	// tunnel; this is a safety net for the physical path. DHCPv6 and neighbour
	// discovery keep the uplink's IPv6 alive; DHCP for IPv4 and ARP go through
	// packet sockets netfilter does not see.
	rules = append(rules,
		nftRule{l4proto: protoUDP, dport: 53, verdict: nftAccept},
		nftRule{l4proto: protoTCP, dport: 53, verdict: nftAccept},
		nftRule{nfproto: 6, l4proto: protoUDP, dport: 547, verdict: nftAccept},
	)
	for typ := uint8(133); typ <= 137; typ++ {
		rules = append(rules, nftRule{nfproto: 6, l4proto: protoICMPv6, icmpType: typ, hasICMPType: true, verdict: nftAccept})
	}
	rules = append(rules, nftRule{verdict: nftDrop})
	return nftChain{name: killSwitchChain, priority: 0, rules: rules}, nil
}

// DisableKillSwitch takes the chain out, and the table with it when the split
// has nothing in it either, and fails when the chain is still there afterwards
// (G08). Nothing there to begin with is success.
func DisableKillSwitch() error {
	slog.Info("disabling kill switch nftables chain")
	if err := nftRemove([]string{killSwitchChain}); err != nil {
		return fmt.Errorf("kill switch не снят: %w", err)
	}
	note(func(j *journal) { j.KillSwitch = false })
	return nil
}

// recoverKillSwitch is the teardown for a record of a run that died: the table
// goes whole, and so does the iptables chain a version before H09 used, should
// the record be one of its. Only the recovery looks for that chain: a session
// of this version never makes one.
func recoverKillSwitch() error {
	err := DisableKillSwitch()
	return errors.Join(err, dropLegacyKillSwitch())
}

// legacyChain is the chain the kill switch lived in before H09.
const legacyChain = "XRAY_KILL"

// legacyCmd is overridable in tests.
var legacyCmd commander = execCommander{}

// dropLegacyKillSwitch takes the old chain out of iptables and ip6tables where
// it is still there. A host without the binaries has none to take out.
func dropLegacyKillSwitch() error {
	var errs []error
	for _, bin := range []string{"iptables", "ip6tables"} {
		if legacyCmd.lookPath(bin) != nil {
			continue
		}
		if _, err := legacyCmd.run(bin, "-w", "-S", legacyChain); err != nil {
			continue // no such chain
		}
		for {
			if _, err := legacyCmd.run(bin, "-w", "-D", "OUTPUT", "-j", legacyChain); err != nil {
				break
			}
		}
		_, _ = legacyCmd.run(bin, "-w", "-F", legacyChain)
		if out, err := legacyCmd.run(bin, "-w", "-X", legacyChain); err != nil {
			errs = append(errs, fmt.Errorf("%s: цепочка %s прежней версии не удалена: %w: %s", bin, legacyChain, err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(errs...)
}
