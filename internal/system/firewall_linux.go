//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"xray-runner/internal/xraycfg"
)

const killSwitchChain = "XRAY_KILL"

// firewallBins covers both IPv4 and IPv6. Leaving ip6tables unmanaged would
// let traffic leak over IPv6 while the kill switch is active.
var firewallBins = []string{"iptables", "ip6tables"}

// fwCmd is overridable in tests.
var fwCmd commander = execCommander{}

func EnableKillSwitch(cfg KillSwitchConfig) error {
	slog.Info("enabling kill switch via iptables/ip6tables", "endpoints", len(cfg.Endpoints))

	// Both families or none (A02): without ip6tables every IPv6 packet goes past
	// the switch, without either nothing is blocked at all — and the session
	// would report it on. Checked before anything is touched.
	var missing []string
	for _, bin := range firewallBins {
		if err := fwCmd.lookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("не найден %s — kill switch требует iptables и ip6tables", strings.Join(missing, ", "))
	}

	// Start from a clean slate so repeated runs don't accumulate duplicate
	// rules or OUTPUT jumps.
	_ = DisableKillSwitch()

	for _, bin := range firewallBins {
		if err := applyKillSwitch(bin, cfg); err != nil {
			// The caller treats a failed enable as "no kill switch" and never
			// tears it down, so a stack that did go in must come out here.
			_ = DisableKillSwitch()
			return err
		}
	}

	return nil
}

func applyKillSwitch(bin string, cfg KillSwitchConfig) error {
	if out, err := fwCmd.run(bin, "-N", killSwitchChain); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("%s create chain: %w\n%s", bin, err, out)
		}
	}

	// Nothing is accepted by conntrack state (A09): an ESTABLISHED accept let
	// every connection opened before the switch keep flowing past it on the
	// physical path. What must pass does so by its own rule below.
	rules := [][]string{
		{"-A", killSwitchChain, "-o", "lo", "-j", "ACCEPT"},
		{"-A", killSwitchChain, "-o", "xray-tun", "-j", "ACCEPT"},
		// Traffic the panel routes `direct` carries this mark and leaves through
		// the physical interface, so the -o xray-tun rule above never covers it.
		// Without an explicit ACCEPT every direct destination hits the final DROP
		// and goes dark. This opens nothing when xray is down: the mark exists
		// only on sockets xray's freedom outbounds create.
		{"-A", killSwitchChain, "-m", "mark", "--mark", strconv.Itoa(xraycfg.DirectFwMark), "-j", "ACCEPT"},
	}

	// Allow xray's own traffic to the VPN servers — every packet of it, with no
	// conntrack accept to lean on — otherwise the uplink, every reconnect and
	// hysteria2's UDP flows are dropped and the tunnel can never recover. Only
	// add the rule to the matching IP stack: an IPv4 -d on ip6tables would fail.
	for _, e := range cfg.Endpoints {
		if rule := serverAcceptRule(bin, e); rule != nil {
			rules = append(rules, rule)
		}
	}

	// Allow DNS so name resolution keeps working while the switch is active
	// (e.g. resolving the server on reconnect). In TUN mode DNS normally goes
	// through the tunnel; this is a safety net for the physical path.
	// The jump goes in at the front, not appended: ufw and friends install their
	// own OUTPUT jumps, and an ACCEPT inside one of them ends traversal of
	// OUTPUT before a jump sitting further down is ever reached — the kill
	// switch would report success and block nothing. It is added last, once the
	// chain above is fully populated, so OUTPUT never points at a half-built
	// chain.
	rules = append(rules,
		[]string{"-A", killSwitchChain, "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
		[]string{"-A", killSwitchChain, "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
		[]string{"-A", killSwitchChain, "-j", "DROP"},
		[]string{"-I", "OUTPUT", "1", "-j", killSwitchChain},
	)

	for _, rule := range rules {
		if out, err := fwCmd.run(bin, rule...); err != nil {
			return fmt.Errorf("%s %s: %w\n%s", bin, rule, err, out)
		}
	}

	return nil
}

// serverAcceptRule builds the ACCEPT rule for one VPN server endpoint, or nil
// if there's no server address or its IP family doesn't match this binary.
func serverAcceptRule(bin string, e Endpoint) []string {
	if e.IP == "" {
		return nil
	}
	ip := net.ParseIP(e.IP)
	if ip == nil {
		return nil
	}
	isV6 := ip.To4() == nil
	if isV6 && bin != "ip6tables" {
		return nil
	}
	if !isV6 && bin != "iptables" {
		return nil
	}
	proto := "tcp"
	if e.UDP {
		proto = "udp"
	}
	return []string{"-A", killSwitchChain, "-d", e.IP, "-p", proto, "--dport", strconv.Itoa(e.Port), "-j", "ACCEPT"}
}

func DisableKillSwitch() error {
	slog.Info("disabling kill switch iptables/ip6tables rules")

	for _, bin := range firewallBins {
		if err := fwCmd.lookPath(bin); err != nil {
			continue
		}
		// Remove every OUTPUT jump: older buggy runs could have appended the
		// jump multiple times, so loop until the delete fails.
		for {
			if _, err := fwCmd.run(bin, "-D", "OUTPUT", "-j", killSwitchChain); err != nil {
				break
			}
		}
		_, _ = fwCmd.run(bin, "-F", killSwitchChain)
		_, _ = fwCmd.run(bin, "-X", killSwitchChain)
	}

	return nil
}
