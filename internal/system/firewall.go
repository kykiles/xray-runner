package system

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

const killSwitchRule = "xray-runner-killswitch"
const killSwitchRuleBlockAll = "xray-runner-blockall"

func EnableKillSwitch() error {
	slog.Info("enabling kill switch firewall rules")

	rules := []struct {
		name string
		dir  string
		args []string
	}{
		{
			name: killSwitchRule,
			dir:  "out",
			args: []string{"advfirewall", "firewall", "add", "rule",
				"name=" + killSwitchRule,
				"dir=out",
				"action=block",
				"remoteip=0.0.0.0/0",
				"enable=yes",
			},
		},
		{
			name: killSwitchRuleBlockAll,
			dir:  "out",
			args: []string{"advfirewall", "firewall", "add", "rule",
				"name=" + killSwitchRuleBlockAll,
				"dir=out",
				"action=block",
				"protocol=any",
				"enable=yes",
			},
		},
	}

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
	for _, name := range []string{killSwitchRule, killSwitchRuleBlockAll} {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
			"name="+name).Run()
	}
	return nil
}
