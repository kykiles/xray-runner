package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/xraycfg"
)

const panelProfile = `{
  "remarks": "Auto",
  "dns": {"servers": [{"address": "77.88.8.8", "domains": ["domain:ru"]}, "1.1.1.1"]},
  "inbounds": [{"tag": "socks-from-panel", "port": 10999, "protocol": "socks"}],
  "outbounds": [
    {"tag": "proxy", "protocol": "vless",
     "settings": {"vnext": [{"address": "a.invalid", "port": 443,
       "users": [{"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "encryption": "none"}]}]},
     "streamSettings": {"network": "tcp", "security": "none"}},
    {"tag": "proxy-2", "protocol": "vless",
     "settings": {"vnext": [{"address": "b.invalid", "port": 443,
       "users": [{"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "encryption": "none"}]}]},
     "streamSettings": {"network": "tcp", "security": "none"}},
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "block", "protocol": "blackhole"}
  ],
  "routing": {
    "balancers": [{"tag": "Balancer", "selector": ["proxy"]}],
    "rules": [
      {"type": "field", "domain": ["2ip.ru"], "outboundTag": "direct"},
      {"type": "field", "domain": ["max.ru"], "outboundTag": "block"},
      {"type": "field", "network": "tcp,udp", "balancerTag": "Balancer"}
    ]
  },
  "burstObservatory": {"subjectSelector": ["proxy"]}
}`

// Picking one server out of a panel profile must keep the panel's routing. This
// is the bug the user hit: 2ip.ru is meant to go out directly, but the session
// was rebuilt from template.json and sent everything into the tunnel.
func TestBuildSessionConfig_SingleServerKeepsPanelRouting(t *testing.T) {
	a := newTemplateApp(t)

	entry := &subscription.SubEntry{
		Remarks: "B", Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		RawOutbound: outboundNamed(t, "proxy-2"),
	}
	raw, ports, err := a.buildSessionConfig(&target{
		profileName: "Auto",
		entry:       entry,
		profileRaw:  json.RawMessage(panelProfile),
	})
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	if ports.socks != 10808 || ports.http != 10809 {
		t.Errorf("ports = %d/%d, want 10808/10809", ports.socks, ports.http)
	}

	var got struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"routing"`
		DNS json.RawMessage `json:"dns"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}

	// The panel's 3 rules plus the probe rule the session prepends so the health
	// check travels the tunnel it reports on.
	if len(got.Routing.Rules) != 4 {
		t.Fatalf("rules = %d, want the panel's 3 plus the probe rule", len(got.Routing.Rules))
	}
	if got.Routing.Rules[0]["domain"] == nil {
		t.Errorf("probe rule is not first: %v", got.Routing.Rules[0])
	}
	if string(got.Routing.Rules[1]["outboundTag"]) != `"direct"` {
		t.Errorf("2ip.ru rule lost: %v", got.Routing.Rules[1])
	}
	if len(got.DNS) == 0 {
		t.Error("panel dns dropped")
	}
	if got.Outbounds[0].Tag != "proxy-2" {
		t.Errorf("outbounds[0] = %q, want the chosen proxy-2", got.Outbounds[0].Tag)
	}
	validateWithXray(t, raw)
}

// A subscription without panel routing (URL list, bare link) still has to work:
// it falls back to template.json plus the catch-all rule.
func TestBuildSessionConfig_NoPanelRoutingFallsBackToTemplate(t *testing.T) {
	a := newTemplateApp(t)

	entry := &subscription.SubEntry{
		Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}
	raw, _, err := a.buildSessionConfig(&target{
		entry:      entry,
		profileRaw: json.RawMessage(`{"outbounds": [{"tag": "proxy", "protocol": "vless"}]}`),
	})
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}

	var got struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if got.Outbounds[0].Tag != "proxy" {
		t.Errorf("outbounds[0] = %q, want the template path's proxy", got.Outbounds[0].Tag)
	}
	validateWithXray(t, raw)
}

func newTemplateApp(t *testing.T) *App {
	t.Helper()
	tc, err := xraycfg.LoadTemplate(filepath.Join(repoRoot(t), "template.json"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	a := New(&config.Config{Mode: "proxy", XrayLogLvl: "warning"}, Options{NonInteractive: true})
	a.template = tc
	return a
}

// outboundNamed pulls one outbound out of the profile, the way the subscription
// parser stores it on the entry.
func outboundNamed(t *testing.T, tag string) json.RawMessage {
	t.Helper()
	var cfg struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(panelProfile), &cfg); err != nil {
		t.Fatalf("panel profile: %v", err)
	}
	for _, o := range cfg.Outbounds {
		var head struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(o, &head); err == nil && head.Tag == tag {
			return o
		}
	}
	t.Fatalf("outbound %q not in the test profile", tag)
	return nil
}

// repoRootDir is resolved at load time: tests chdir into a scratch directory,
// and "../.." means nothing from there.
var repoRootDir, repoRootErr = filepath.Abs("../..")

func repoRoot(t *testing.T) string {
	t.Helper()
	if repoRootErr != nil {
		t.Fatalf("abs: %v", repoRootErr)
	}
	return repoRootDir
}

// validateWithXray lets xray judge the generated config when the binary is here.
func validateWithXray(t *testing.T, raw []byte) {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(root, "xray_linux", "xray")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("xray binary not present, skipping config validation")
	}
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(bin, "-test", "-config", cfgPath)
	cmd.Dir = root // geoip.dat/geosite.dat live there
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xray rejected the generated config: %v\n%s", err, out)
	}
}

// The scripted path (--server) must apply the panel's routing too: a cron job or
// a systemd unit connects the same subscription as the menu does.
func TestResolveScriptedTarget_KeepsPanelRouting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + panelProfile + "]"))
	}))
	defer srv.Close()

	tc, err := xraycfg.LoadTemplate(filepath.Join(repoRoot(t), "template.json"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	isolateState(t)
	if err := os.WriteFile("subscriptions.txt", []byte(srv.URL+"\n"), 0600); err != nil {
		t.Fatalf("write subscriptions: %v", err)
	}

	a := New(&config.Config{Mode: "proxy", XrayLogLvl: "warning"}, Options{Server: "2", NonInteractive: true})
	a.template = tc

	tgt, err := a.resolveScriptedTarget()
	if err != nil {
		t.Fatalf("resolveScriptedTarget: %v", err)
	}
	if tgt.entry == nil || tgt.entry.Address != "b.invalid" {
		t.Fatalf("--server 2 picked %+v, want the second server", tgt.entry)
	}
	if len(tgt.profileRaw) == 0 {
		t.Fatal("scripted target carries no profile: the panel's routing is lost")
	}
	// TUN must keep every server of the profile outside the tunnel.
	if hosts := tgt.serverHosts(); len(hosts) != 2 {
		t.Errorf("serverHosts = %v, want both servers of the profile", hosts)
	}

	raw, _, err := a.buildSessionConfig(tgt)
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	var got struct {
		Routing struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if len(got.Routing.Rules) != 4 {
		t.Fatalf("rules = %d, want the panel's 3 plus the probe rule", len(got.Routing.Rules))
	}
	if string(got.Routing.Rules[1]["outboundTag"]) != `"direct"` {
		t.Errorf("2ip.ru rule lost in the scripted path: %v", got.Routing.Rules[1])
	}
	validateWithXray(t, raw)
}
