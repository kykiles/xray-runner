//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

const killSwitchChain = "XRAY_KILL"

func EnableKillSwitch() error {
	slog.Info("enabling kill switch via iptables")

	createChain := exec.Command("iptables", "-N", killSwitchChain)
	if out, err := createChain.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("create iptables chain: %w\n%s", err, out)
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
		args := append([]string{}, rule...)
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("iptables %s: %w\n%s", args, err, out)
		}
	}

	return nil
}

func DisableKillSwitch() error {
	slog.Info("disabling kill switch iptables rules")

	exec.Command("iptables", "-D", "OUTPUT", "-j", killSwitchChain).Run()
	exec.Command("iptables", "-F", killSwitchChain).Run()
	exec.Command("iptables", "-X", killSwitchChain).Run()

	return nil
}
