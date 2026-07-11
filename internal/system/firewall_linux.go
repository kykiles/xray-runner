//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

const killSwitchChain = "XRAY_KILL"

// firewallBins covers both IPv4 and IPv6. Leaving ip6tables unmanaged would
// let traffic leak over IPv6 while the kill switch is active.
var firewallBins = []string{"iptables", "ip6tables"}

// commander abstracts firewall command execution so the kill-switch logic can
// be unit-tested without root privileges or a real iptables binary.
type commander interface {
	lookPath(bin string) error
	run(bin string, args ...string) ([]byte, error)
}

type execCommander struct{}

func (execCommander) lookPath(bin string) error {
	_, err := exec.LookPath(bin)
	return err
}

func (execCommander) run(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).CombinedOutput()
}

// fwCmd is overridable in tests.
var fwCmd commander = execCommander{}

func EnableKillSwitch() error {
	slog.Info("enabling kill switch via iptables/ip6tables")

	// Start from a clean slate so repeated runs don't accumulate duplicate
	// rules or OUTPUT jumps.
	DisableKillSwitch()

	for _, bin := range firewallBins {
		if err := fwCmd.lookPath(bin); err != nil {
			slog.Warn("firewall binary not found, skipping", "bin", bin)
			continue
		}
		if err := applyKillSwitch(bin); err != nil {
			return err
		}
	}

	return nil
}

func applyKillSwitch(bin string) error {
	if out, err := fwCmd.run(bin, "-N", killSwitchChain); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("%s create chain: %w\n%s", bin, err, out)
		}
	}

	rules := [][]string{
		{"-A", killSwitchChain, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-A", killSwitchChain, "-o", "lo", "-j", "ACCEPT"},
		{"-A", killSwitchChain, "-o", "xray-tun", "-j", "ACCEPT"},
		{"-A", killSwitchChain, "-j", "DROP"},
		{"-A", "OUTPUT", "-j", killSwitchChain},
	}

	for _, rule := range rules {
		if out, err := fwCmd.run(bin, rule...); err != nil {
			return fmt.Errorf("%s %s: %w\n%s", bin, rule, err, out)
		}
	}

	return nil
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
		fwCmd.run(bin, "-F", killSwitchChain)
		fwCmd.run(bin, "-X", killSwitchChain)
	}

	return nil
}
