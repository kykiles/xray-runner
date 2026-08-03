package xraycfg

import (
	"encoding/json"
	"testing"
)

func rules(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var cfg struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	return cfg.Routing.Rules
}

// A profile with a balancer: the probe has to go through the balancer, or it
// measures whatever the panel's own rules decide — usually the plain internet.
func TestPrependProbeRuleAimsAtTheBalancer(t *testing.T) {
	raw := json.RawMessage(`{
		"outbounds": [{"tag": "proxy-1"}, {"tag": "direct"}],
		"routing": {
			"balancers": [{"tag": "balancer"}],
			"rules": [{"type": "field", "domain": ["geosite:category-ru"], "outboundTag": "direct"}]
		}
	}`)

	out, err := PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}

	got := rules(t, out)
	if len(got) != 2 {
		t.Fatalf("rules = %d, want the panel's 1 plus the probe rule", len(got))
	}
	if got[0]["balancerTag"] != "balancer" {
		t.Errorf("probe rule = %v, want it aimed at the balancer and placed first", got[0])
	}
	if got[1]["outboundTag"] != "direct" {
		t.Errorf("panel rule lost: %v", got[1])
	}
}

// Without a balancer the probe follows the first outbound, which is where
// unmatched traffic falls through to.
func TestPrependProbeRuleAimsAtTheFirstOutbound(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "proxy"}, {"tag": "direct"}]}`)

	out, err := PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	got := rules(t, out)
	if len(got) != 1 || got[0]["outboundTag"] != "proxy" {
		t.Errorf("rules = %v, want one rule aimed at proxy", got)
	}
}

// Nothing to aim at: writing a rule pointing at a tag that does not exist would
// stop xray from starting at all, so the config is left alone.
func TestPrependProbeRuleLeavesUntaggedConfigAlone(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"protocol": "freedom"}]}`)

	out, err := PrependProbeRule(raw, []string{"full:www.google.com"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	if string(out) != string(raw) {
		t.Errorf("config = %s, want it unchanged", out)
	}
	if out, _ := PrependProbeRule(raw, nil); string(out) != string(raw) {
		t.Errorf("config = %s, want it unchanged without probe hosts", out)
	}
}
