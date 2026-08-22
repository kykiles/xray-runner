package xraycfg

import (
	"encoding/json"
	"errors"
	"testing"
)

// rulesOf returns the routing rules of a built config, decoded loosely so a
// test can assert on the keys it cares about.
func rulesOf(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return cfg.Routing.Rules
}

// A panel config: blocked hosts, a direct rule for the local country, and a
// catch-all into the balancer. Split routing must keep the first two, aim the
// process rule at the balancer, and end with a direct catch-all.
const panelConfig = `{
  "outbounds": [
    {"tag": "proxy-a", "protocol": "vless"},
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "block", "protocol": "blackhole"}
  ],
  "routing": {
    "balancers": [{"tag": "balancer", "selector": ["proxy"]}],
    "rules": [
      {"domain": ["geosite:category-ads"], "outboundTag": "block"},
      {"domain": ["geosite:ru"], "outboundTag": "direct"},
      {"network": "tcp,udp", "balancerTag": "balancer"}
    ]
  }
}`

func TestApplySplitRoutingKeepsLocalRulesAndAimsAtBalancer(t *testing.T) {
	raw, err := ApplySplitRouting(json.RawMessage(panelConfig), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if len(rules) != 4 {
		t.Fatalf("got %d rules, want 4: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "block" || rules[1]["outboundTag"] != "direct" {
		t.Errorf("panel's local rules not kept in order: %v", rules[:2])
	}

	// The process rule follows them, pointing where the panel's catch-all did.
	procs, ok := rules[2]["process"].([]any)
	if !ok || len(procs) != 1 || procs[0] != "Telegram.exe" {
		t.Fatalf("process rule = %v, want the listed process", rules[2])
	}
	if rules[2]["balancerTag"] != "balancer" {
		t.Errorf("process rule aims at %v, want the panel's balancer", rules[2])
	}

	// And everything unmatched — every process outside the list — goes direct.
	last := rules[3]
	if last["outboundTag"] != "direct" || last["network"] != "tcp,udp" {
		t.Errorf("catch-all = %v, want tcp,udp to direct", last)
	}
}

// The rule the panel used to reach the tunnel must not survive: it would send
// the whole system through the tunnel regardless of which process opened the
// connection.
func TestApplySplitRoutingDropsTunnelRules(t *testing.T) {
	raw, err := ApplySplitRouting(json.RawMessage(panelConfig), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	for _, r := range rulesOf(t, raw) {
		if r["process"] != nil {
			continue
		}
		if r["balancerTag"] != nil {
			t.Errorf("a rule other than the process one still reaches the tunnel: %v", r)
		}
	}
}

// A single-server config: no balancer, the catch-all names the proxy outbound.
func TestApplySplitRoutingAimsAtOutboundTag(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {"rules": [{"network": "tcp,udp", "outboundTag": "proxy"}]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"chrome.exe", "claude.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want the process rule and the catch-all: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "proxy" || len(rules[0]["process"].([]any)) != 2 {
		t.Errorf("process rule = %v, want both processes routed to proxy", rules[0])
	}
	if rules[1]["outboundTag"] != "direct" {
		t.Errorf("catch-all = %v, want direct", rules[1])
	}
}

// A panel with no freedom outbound has no way out except the tunnel, which is
// the one thing unlisted traffic must not take — so one is added.
func TestApplySplitRoutingAddsDirectOutbound(t *testing.T) {
	cfg := `{
      "outbounds": [{"tag": "proxy", "protocol": "vless"}],
      "routing": {"rules": [{"network": "tcp,udp", "outboundTag": "proxy"}]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"chrome.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	var got struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 2 || got.Outbounds[1].Protocol != "freedom" {
		t.Fatalf("outbounds = %v, want a freedom one appended", got.Outbounds)
	}
	rules := rulesOf(t, raw)
	if rules[len(rules)-1]["outboundTag"] != got.Outbounds[1].Tag {
		t.Errorf("catch-all does not point at the added freedom outbound: %v", rules)
	}
}

// Nothing to aim the process rule at means the config would silently route the
// listed processes nowhere — refuse instead.
func TestApplySplitRoutingWithoutTunnelTarget(t *testing.T) {
	cfg := `{
      "outbounds": [{"tag": "direct", "protocol": "freedom"}],
      "routing": {"rules": [{"domain": ["geosite:ru"], "outboundTag": "direct"}]}
    }`
	_, err := ApplySplitRouting(json.RawMessage(cfg), []string{"chrome.exe"})
	if !errors.Is(err, ErrNoTunnelTarget) {
		t.Fatalf("err = %v, want ErrNoTunnelTarget", err)
	}
}

// A rule with neither balancerTag nor outboundTag rides the default outbound.
// It used to slip past the guard and emit "outboundTag": null, which xray
// rejects — the user saw an opaque config error instead of the proxy fallback.
func TestApplySplitRoutingWithUntaggedRule(t *testing.T) {
	cfg := `{
      "outbounds": [{"tag": "direct", "protocol": "freedom"}],
      "routing": {"rules": [{"domain": ["geosite:ru"]}]}
    }`
	_, err := ApplySplitRouting(json.RawMessage(cfg), []string{"chrome.exe"})
	if !errors.Is(err, ErrNoTunnelTarget) {
		t.Fatalf("err = %v, want ErrNoTunnelTarget", err)
	}
}

func TestApplySplitRoutingWithoutProcesses(t *testing.T) {
	if _, err := ApplySplitRouting(json.RawMessage(panelConfig), nil); err == nil {
		t.Fatal("an empty process list must be refused: it would tunnel nothing")
	}
}
