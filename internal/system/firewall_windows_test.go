//go:build windows

package system

import (
	"errors"
	"strings"
	"testing"
)

// fakeNetsh records every netsh invocation so the rules the kill switch
// installs can be inspected without touching the host firewall.
type fakeNetsh struct {
	cmds []string
	out  string
	err  error
}

func (f *fakeNetsh) lookPath(bin string) error { return nil }

func (f *fakeNetsh) run(bin string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, bin+" "+strings.Join(args, " "))
	return []byte(f.out), f.err
}

// rule returns the recorded invocation that carries name=<want>.
func (f *fakeNetsh) rule(name string) (string, bool) {
	for _, c := range f.cmds {
		if strings.Contains(c, "name="+name+" ") || strings.HasSuffix(c, "name="+name) {
			return c, true
		}
	}
	return "", false
}

func withFakeNetsh(t *testing.T, f *fakeNetsh) {
	t.Helper()
	orig := fwCmd
	fwCmd = f
	t.Cleanup(func() { fwCmd = orig })
}

var ksCfg = KillSwitchConfig{XrayPath: `C:\xray\xray.exe`}

// The block rule must reach both IP families. Scoping it to 0.0.0.0/0 covered
// IPv4 only and let every IPv6 destination past the kill switch, while the user
// was told they were protected.
func TestEnableKillSwitch_BlocksBothIPFamilies(t *testing.T) {
	f := &fakeNetsh{}
	withFakeNetsh(t, f)

	if err := EnableKillSwitch(ksCfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	block, ok := f.rule(killSwitchRule)
	if !ok {
		t.Fatalf("no block rule was added, got: %v", f.cmds)
	}
	if strings.Contains(block, "remoteip=0.0.0.0/0") {
		t.Errorf("block rule is scoped to IPv4, so IPv6 leaks past it: %q", block)
	}
	if !strings.Contains(block, "action=block") || !strings.Contains(block, "dir=out") {
		t.Errorf("block rule must block outbound traffic, got: %q", block)
	}
}

// Windows Firewall gives allow-rules precedence over block-rules, but only
// rules that exist: if the block rule lands while an allow rule failed, xray's
// own uplink is cut and the tunnel can never come back.
func TestEnableKillSwitch_AllowsXrayAndLoopback(t *testing.T) {
	f := &fakeNetsh{}
	withFakeNetsh(t, f)

	if err := EnableKillSwitch(ksCfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	xray, ok := f.rule(killSwitchRuleAllowXray)
	if !ok {
		t.Fatalf("no allow rule for xray, got: %v", f.cmds)
	}
	if !strings.Contains(xray, `program=C:\xray\xray.exe`) {
		t.Errorf("allow rule must name the xray binary, got: %q", xray)
	}
	if _, ok := f.rule(killSwitchRuleAllowLoopback); !ok {
		t.Fatalf("no allow rule for loopback, got: %v", f.cmds)
	}
}

// Without a known binary path there is nothing to allow by program, and adding
// a rule with an empty program= would match everything.
func TestEnableKillSwitch_SkipsXrayRuleWithoutPath(t *testing.T) {
	f := &fakeNetsh{}
	withFakeNetsh(t, f)

	if err := EnableKillSwitch(KillSwitchConfig{}); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}

	if _, ok := f.rule(killSwitchRuleAllowXray); ok {
		t.Errorf("no xray allow rule may be added without a path, got: %v", f.cmds)
	}
}

// A previous session that crashed leaves its rules behind; netsh reports them
// as already existing, which must not abort the install.
func TestEnableKillSwitch_TreatsExistingRulesAsSuccess(t *testing.T) {
	f := &fakeNetsh{out: "Ok.\nThe object already exists.", err: errors.New("exit status 1")}
	withFakeNetsh(t, f)

	if err := EnableKillSwitch(ksCfg); err != nil {
		t.Fatalf("existing rules must not fail the install: %v", err)
	}
}

func TestEnableKillSwitch_FailsOnRealError(t *testing.T) {
	f := &fakeNetsh{out: "The parameter is incorrect.", err: errors.New("exit status 1")}
	withFakeNetsh(t, f)

	if err := EnableKillSwitch(ksCfg); err == nil {
		t.Fatal("expected an error when netsh rejects the rule")
	}
}

// Every rule must go, including the allow-rules: a leftover allow for the xray
// binary would silently widen the next session's firewall.
func TestDisableKillSwitch_RemovesEveryRule(t *testing.T) {
	f := &fakeNetsh{}
	withFakeNetsh(t, f)

	if err := DisableKillSwitch(); err != nil {
		t.Fatalf("DisableKillSwitch: %v", err)
	}

	for _, name := range []string{killSwitchRule, killSwitchRuleAllowXray, killSwitchRuleAllowLoopback} {
		if _, ok := f.rule(name); !ok {
			t.Errorf("rule %s was not deleted, got: %v", name, f.cmds)
		}
	}
}
