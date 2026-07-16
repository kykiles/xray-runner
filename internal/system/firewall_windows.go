//go:build windows

package system

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

const killSwitchRule = "xray-runner-killswitch"
const killSwitchRuleAllowXray = "xray-runner-allow-xray"
const killSwitchRuleAllowLoopback = "xray-runner-allow-loopback"

func EnableKillSwitch(cfg KillSwitchConfig) error {
	slog.Info("enabling kill switch firewall rules", "xray", cfg.XrayPath)

	// Windows Firewall evaluates allow-rules before block-rules, so these
	// allow-rules take precedence: xray's own traffic and loopback stay open
	// while the single block-rule below drops everything else. (The old
	// protocol=any block-rule also blocked xray itself — removed.)
	rules := []struct {
		name string
		args []string
	}{
		{
			name: killSwitchRuleAllowLoopback,
			args: []string{"advfirewall", "firewall", "add", "rule",
				"name=" + killSwitchRuleAllowLoopback,
				"dir=out",
				"action=allow",
				"remoteip=127.0.0.1,::1",
				"enable=yes",
			},
		},
	}

	if cfg.XrayPath != "" {
		rules = append(rules, struct {
			name string
			args []string
		}{
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

	rules = append(rules, struct {
		name string
		args []string
	}{
		name: killSwitchRule,
		args: []string{"advfirewall", "firewall", "add", "rule",
			"name=" + killSwitchRule,
			"dir=out",
			"action=block",
			"remoteip=0.0.0.0/0",
			"enable=yes",
		},
	})

	for _, rule := range rules {
		if out, err := exec.Command("netsh", rule.args...).CombinedOutput(); err != nil {
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
	for _, name := range []string{killSwitchRule, killSwitchRuleAllowXray, killSwitchRuleAllowLoopback} {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
			"name="+name).Run()
	}
	return nil
}
