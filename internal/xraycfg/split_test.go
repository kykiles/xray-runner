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
	if len(rules) != 5 {
		t.Fatalf("got %d rules, want 5: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "block" || rules[1]["outboundTag"] != "direct" {
		t.Errorf("panel's local rules not kept in order: %v", rules[:2])
	}

	// The panel's catch-all follows them, narrowed to the listed process.
	procs, ok := rules[2]["process"].([]any)
	if !ok || len(procs) != 1 || procs[0] != "Telegram.exe" {
		t.Fatalf("process rule = %v, want the listed process", rules[2])
	}
	if rules[2]["balancerTag"] != "balancer" || rules[2]["network"] != "tcp,udp" {
		t.Errorf("process rule = %v, want the panel's catch-all into the balancer", rules[2])
	}
	// Then the listed process's default: the first outbound.
	if rules[3]["outboundTag"] != "proxy-a" || rules[3]["process"] == nil {
		t.Errorf("default rule = %v, want the listed process to proxy-a", rules[3])
	}

	// And everything unmatched — every process outside the list — goes direct.
	last := rules[4]
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
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want the narrowed rule, the default and the catch-all: %v", len(rules), rules)
	}
	if rules[0]["outboundTag"] != "proxy" || len(rules[0]["process"].([]any)) != 2 {
		t.Errorf("process rule = %v, want both processes routed to proxy", rules[0])
	}
	if rules[1]["outboundTag"] != "proxy" || len(rules[1]["process"].([]any)) != 2 {
		t.Errorf("default rule = %v, want both processes routed to proxy", rules[1])
	}
	if rules[2]["outboundTag"] != "direct" {
		t.Errorf("catch-all = %v, want direct", rules[2])
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

// ruleWith finds the first rule whose key holds value, failing the test when
// there is none.
func ruleWith(t *testing.T, rules []map[string]any, key, value string) map[string]any {
	t.Helper()
	for _, r := range rules {
		if r[key] == value {
			return r
		}
	}
	t.Fatalf("no rule with %s=%s in %v", key, value, rules)
	return nil
}

// A second freedom outbound under its own tag is as local as the first: a rule
// into it is kept for everyone and never becomes where the listed processes go.
func TestApplySplitRoutingSecondFreedomIsLocal(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"},
        {"tag": "bypass", "protocol": "freedom"}
      ],
      "routing": {"rules": [
        {"domain": ["example.com"], "outboundTag": "proxy"},
        {"network": "tcp,udp", "outboundTag": "bypass"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if rules[0]["outboundTag"] != "proxy" || rules[0]["process"] == nil {
		t.Errorf("rule 0 = %v, want example.com to proxy for the listed process", rules[0])
	}
	if rules[1]["outboundTag"] != "bypass" || rules[1]["process"] != nil {
		t.Errorf("rule 1 = %v, want the panel's bypass rule kept as it was", rules[1])
	}
	for _, r := range rules {
		if r["process"] != nil && r["outboundTag"] == "bypass" {
			t.Errorf("listed processes sent to the second freedom outbound: %v", r)
		}
	}
}

// Same for a second blackhole: aiming the listed processes at it would cut them
// off altogether.
func TestApplySplitRoutingSecondBlackholeIsLocal(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"},
        {"tag": "block", "protocol": "blackhole"},
        {"tag": "adblock", "protocol": "blackhole"}
      ],
      "routing": {"rules": [
        {"network": "tcp,udp", "outboundTag": "proxy"},
        {"domain": ["geosite:category-ads"], "outboundTag": "adblock"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	for _, r := range rulesOf(t, raw) {
		if r["process"] != nil && r["outboundTag"] == "adblock" {
			t.Errorf("listed processes sent to the second blackhole: %v", r)
		}
	}
}

// Two balancers for two sets of domains: each set keeps its balancer, in the
// panel's order, and the panel's direct rule stays behind them.
func TestApplySplitRoutingKeepsEveryBalancerRule(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "us1", "protocol": "vless"},
        {"tag": "de1", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {
        "balancers": [{"tag": "US", "selector": ["us"]}, {"tag": "DE", "selector": ["de"]}],
        "rules": [
          {"domain": ["netflix.com"], "balancerTag": "US"},
          {"domain": ["spiegel.de"], "balancerTag": "DE"},
          {"domain": ["geosite:ru"], "outboundTag": "direct"}
        ]
      }
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	if len(rules) != 5 {
		t.Fatalf("got %d rules, want 5: %v", len(rules), rules)
	}
	want := []struct{ key, tag, domain string }{
		{"balancerTag", "US", "netflix.com"},
		{"balancerTag", "DE", "spiegel.de"},
		{"outboundTag", "direct", "geosite:ru"},
	}
	for i, w := range want {
		r := rules[i]
		d, _ := r["domain"].([]any)
		if r[w.key] != w.tag || len(d) != 1 || d[0] != w.domain {
			t.Errorf("rule %d = %v, want %s → %s", i, r, w.domain, w.tag)
		}
		if (w.key == "balancerTag") != (r["process"] != nil) {
			t.Errorf("rule %d = %v: only the tunnel rules are narrowed to the process", i, r)
		}
	}
	if rules[3]["outboundTag"] != "us1" || rules[3]["process"] == nil {
		t.Errorf("default rule = %v, want the listed process to the first outbound", rules[3])
	}
	if rules[4]["outboundTag"] != "direct" || rules[4]["process"] != nil {
		t.Errorf("catch-all = %v, want everything else direct", rules[4])
	}
}

// A panel with no rule into the tunnel relies on the first outbound: the
// listed processes go there instead of the whole thing being refused.
func TestApplySplitRoutingDefaultsToFirstOutbound(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {"rules": [{"domain": ["geosite:ru"], "outboundTag": "direct"}]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"Telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	r := ruleWith(t, rulesOf(t, raw), "outboundTag", "proxy")
	if r["process"] == nil {
		t.Errorf("default rule = %v, want it narrowed to the listed process", r)
	}
}

// A panel rule naming processes of its own keeps only the listed ones, and
// goes when none of them is listed.
func TestApplySplitRoutingIntersectsRuleProcesses(t *testing.T) {
	cfg := `{
      "outbounds": [
        {"tag": "proxy", "protocol": "vless"},
        {"tag": "direct", "protocol": "freedom"}
      ],
      "routing": {"rules": [
        {"process": ["Telegram.exe", "chrome.exe"], "domain": ["a.example"], "outboundTag": "proxy"},
        {"process": ["firefox.exe"], "domain": ["b.example"], "outboundTag": "proxy"}
      ]}
    }`
	raw, err := ApplySplitRouting(json.RawMessage(cfg), []string{"telegram.exe"})
	if err != nil {
		t.Fatalf("ApplySplitRouting: %v", err)
	}
	rules := rulesOf(t, raw)
	procs, _ := rules[0]["process"].([]any)
	if len(procs) != 1 || procs[0] != "Telegram.exe" {
		t.Errorf("rule 0 = %v, want its process list cut to Telegram.exe", rules[0])
	}
	for _, r := range rules {
		if d, _ := r["domain"].([]any); len(d) == 1 && d[0] == "b.example" {
			t.Errorf("a rule for unlisted processes only survived: %v", r)
		}
	}
}
