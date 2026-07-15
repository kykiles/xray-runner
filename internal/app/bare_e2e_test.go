package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/xraycfg"
)

// A bare link must survive the whole scripted path — list → target → xray
// config — without ever reaching the network. The link points at a host that
// does not resolve, so any fetch attempt fails the test.
func TestBareLink_ScriptedTargetToConfig(t *testing.T) {
	const link = "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@nonexistent.invalid:8080?type=ws&security=none&path=%2Fvless-ws#GERMANY%203"

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	tc, err := xraycfg.LoadTemplate(filepath.Join(repoRoot, "template.json"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	// subscriptions.txt and last_server.json are resolved relative to the
	// working directory, so the test runs in a scratch dir.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("subscriptions.txt", []byte(link+"\n"), 0600); err != nil {
		t.Fatalf("write subscriptions: %v", err)
	}

	a := New(&config.Config{Mode: "proxy", XrayLogLvl: "warning"}, Options{Server: "1", NonInteractive: true})
	a.template = tc

	target, err := a.resolveScriptedTarget()
	if err != nil {
		t.Fatalf("resolveScriptedTarget: %v", err)
	}
	if target.entry == nil {
		t.Fatal("target.entry is nil: a bare link must run as a single server, not a profile")
	}
	if target.entry.Address != "nonexistent.invalid" || target.entry.Port != 8080 {
		t.Errorf("endpoint = %s:%d, want nonexistent.invalid:8080", target.entry.Address, target.entry.Port)
	}
	if target.title() != "GERMANY 3" {
		t.Errorf("title = %q, want %q", target.title(), "GERMANY 3")
	}

	raw, ports, err := a.buildSessionConfig(target)
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if ports.socks != 10808 || ports.http != 10809 {
		t.Errorf("ports = %d/%d, want 10808/10809", ports.socks, ports.http)
	}

	// Let xray itself judge the generated config, when it is available here.
	bin := filepath.Join(repoRoot, "xray_linux", "xray")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("xray binary not present, skipping config validation")
	}
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(bin, "-test", "-config", cfgPath)
	cmd.Dir = repoRoot // geoip.dat/geosite.dat live there
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xray rejected the config from a bare link: %v\n%s", err, out)
	}
}
