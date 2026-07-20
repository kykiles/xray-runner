//go:build windows

package system

import (
	"fmt"
	"log/slog"
	"strings"
)

const killSwitchRule = "xray-runner-killswitch"
const killSwitchRuleAllowXray = "xray-runner-allow-xray"
const killSwitchRuleAllowLoopback = "xray-runner-allow-loopback"

// fwCmd is overridable in tests.
var fwCmd commander = execCommander{}

// firewallRule is one netsh rule: the name is carried separately so a failure
// can say which rule broke.
type firewallRule struct {
	name string
	args []string
}

func EnableKillSwitch(cfg KillSwitchConfig) error {
	slog.Info("enabling kill switch firewall rules", "xray", cfg.XrayPath)

	// Windows Firewall evaluates allow-rules before block-rules, so these
	// allow-rules take precedence: xray's own traffic and loopback stay open
	// while the single block-rule below drops everything else. (The old
	// protocol=any block-rule also blocked xray itself — removed.)
	rules := []firewallRule{{
		name: killSwitchRuleAllowLoopback,
		args: []string{"advfirewall", "firewall", "add", "rule",
			"name=" + killSwitchRuleAllowLoopback,
			"dir=out",
			"action=allow",
			"remoteip=127.0.0.1,::1",
			"enable=yes",
		},
	}}

	if cfg.XrayPath != "" {
		rules = append(rules, firewallRule{
			name: killSwitchRuleAllowXray,
			args: []string{"advfirewall", "firewall", "add", "rule",
				"name=" + killSwitchRuleAllowXray,
				"dir=out",
				"action=allow",
				"program=" + cfg.XrayPath,
				"enable=yes",
			},
		})
	}

	// No remoteip: netsh then blocks every remote address in both IP families.
	// Pinning it to 0.0.0.0/0 covered IPv4 only and let the whole of IPv6 walk
	// past the kill switch — the leak firewall_linux.go avoids by managing
	// ip6tables alongside iptables.
	rules = append(rules, firewallRule{
		name: killSwitchRule,
		args: []string{"advfirewall", "firewall", "add", "rule",
			"name=" + killSwitchRule,
			"dir=out",
			"action=block",
			"enable=yes",
		},
	})

	for _, rule := range rules {
		if out, err := fwCmd.run("netsh", rule.args...); err != nil {
			if strings.Contains(string(out), "already exists") {
				continue
			}
			return fmt.Errorf("add firewall rule %s: %w\n%s", rule.name, err, out)
		}
	}
	return nil
}

func DisableKillSwitch() error {
	slog.Info("disabling kill switch firewall rules")
	// Best-effort: teardown also runs for sessions that never enabled the kill
	// switch, where there is nothing to delete.
	for _, name := range []string{killSwitchRule, killSwitchRuleAllowXray, killSwitchRuleAllowLoopback} {
		_, _ = fwCmd.run("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name)
	}
	return nil
}
