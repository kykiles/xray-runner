package xraycfg

import (
	"encoding/json"
	"reflect"
	"strings"
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

// F04: a request to an IP URL carries no name, so a "full:" matcher never fires
// for it and the probe fell through to the profile's own direct rule. An IP
// target needs a rule of its own — a separate one, because a rule's domain and
// ip conditions have to be satisfied together, not either-or.
func TestPrependProbeRuleSplitsDomainsAndIPs(t *testing.T) {
	raw := json.RawMessage(`{
		"outbounds": [{"tag": "proxy"}, {"tag": "direct"}],
		"routing": {"rules": [{"type": "field", "ip": ["127.0.0.0/8"], "outboundTag": "direct"}]}
	}`)

	out, err := PrependProbeRule(raw, []string{"full:www.google.com", "127.0.0.1", "::1"})
	if err != nil {
		t.Fatalf("PrependProbeRule() = %v, want nil", err)
	}
	got := rules(t, out)
	if len(got) != 3 {
		t.Fatalf("rules = %d (%v), want the panel's 1 plus a domain rule and an ip rule", len(got), got)
	}
	domainRule, ipRule, panelRule := got[0], got[1], got[2]

	if !reflect.DeepEqual(domainRule["domain"], []any{"full:www.google.com"}) {
		t.Errorf("domain rule = %v", domainRule)
	}
	// Exact networks: /32 and /128 match that address and no neighbour of it.
	if !reflect.DeepEqual(ipRule["ip"], []any{"127.0.0.1/32", "::1/128"}) {
		t.Errorf("ip rule = %v", ipRule)
	}
	for _, r := range []map[string]any{domainRule, ipRule} {
		if _, hasDomain := r["domain"]; hasDomain {
			if _, hasIP := r["ip"]; hasIP {
				t.Errorf("one rule carries domain and ip at once, which matches neither target: %v", r)
			}
		}
		if r["type"] != "field" || r["outboundTag"] != "proxy" {
			t.Errorf("probe rule = %v, want a field rule aimed at proxy", r)
		}
	}
	if panelRule["outboundTag"] != "direct" {
		t.Errorf("panel rule lost or reordered: %v", panelRule)
	}
}

// A kind with nothing in it gets no rule at all: a rule carrying an empty
// domain or ip list matches everything, which would put a catch-all ahead of
// the profile's own routing.
func TestPrependProbeRuleWritesNoEmptyRule(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "proxy"}]}`)
	tests := []struct {
		name, present, absent string
		hosts                 []string
	}{
		{"only domains", "domain", "ip", []string{"full:www.google.com"}},
		{"only ips", "ip", "domain", []string{"127.0.0.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := PrependProbeRule(raw, tt.hosts)
			if err != nil {
				t.Fatalf("PrependProbeRule() = %v, want nil", err)
			}
			got := rules(t, out)
			if len(got) != 1 {
				t.Fatalf("rules = %v, want exactly one", got)
			}
			if _, ok := got[0][tt.present]; !ok {
				t.Errorf("rule = %v, want it to carry %s", got[0], tt.present)
			}
			if _, ok := got[0][tt.absent]; ok {
				t.Errorf("empty %s list would match everything: %v", tt.absent, got[0])
			}
		})
	}
}

// A zone identifier names one of this host's own interfaces and means nothing
// to a routing rule. Turning it into a domain matcher would leave the probe
// matching nothing, silently — the very failure this rule exists to prevent.
func TestPrependProbeRuleRefusesZonedIPv6(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "proxy"}]}`)

	out, err := PrependProbeRule(raw, []string{"fe80::1%eth0"})
	if err == nil {
		t.Fatalf("a zoned address passed through: %s", out)
	}
	if !strings.Contains(err.Error(), "eth0") {
		t.Errorf("error does not name the zone: %v", err)
	}
}
