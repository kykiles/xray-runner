package xraycfg

import (
	"encoding/json"
	"errors"
	"testing"
)

// A balancer profile must keep its own outbounds, routing, balancers and
// observatory: those are what make the balancing work. Only the inbounds are
// ours, since the profile's inbounds target another client.
func TestMergeProfile_KeepsRoutingAndBalancers(t *testing.T) {
	raw := json.RawMessage(`{
	  "remarks": "Auto",
	  "dns": {"servers": ["1.1.1.1"]},
	  "inbounds": [{"tag": "socks-from-panel", "port": 10999, "protocol": "socks"}],
	  "outbounds": [
	    {"tag": "proxy", "protocol": "vless"},
	    {"tag": "proxy-2", "protocol": "vless"},
	    {"tag": "direct", "protocol": "freedom"}
	  ],
	  "routing": {"balancers": [{"tag": "Balancer", "selector": ["proxy"]}],
	              "rules": [{"type": "field", "ip": ["1.1.1.1"], "balancerTag": "Balancer"}]},
	  "burstObservatory": {"subjectSelector": ["proxy"]}
	}`)

	inbounds := []Inbound{{Tag: "socks", Port: 10808, Protocol: "socks"}}
	cfg, err := MergeProfile(raw, inbounds, "warning")
	if err != nil {
		t.Fatalf("MergeProfile: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Our inbounds replaced the panel's.
	var gotInbounds []Inbound
	if err := json.Unmarshal(got["inbounds"], &gotInbounds); err != nil {
		t.Fatalf("inbounds: %v", err)
	}
	if len(gotInbounds) != 1 || gotInbounds[0].Port != 10808 || gotInbounds[0].Tag != "socks" {
		t.Errorf("inbounds not replaced: %+v", gotInbounds)
	}

	// The profile's own machinery survived.
	var routing map[string]interface{}
	if err := json.Unmarshal(got["routing"], &routing); err != nil {
		t.Fatalf("routing: %v", err)
	}
	if _, ok := routing["balancers"]; !ok {
		t.Error("balancers dropped from routing")
	}
	if _, ok := got["burstObservatory"]; !ok {
		t.Error("burstObservatory dropped")
	}
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(got["outbounds"], &outbounds); err != nil {
		t.Fatalf("outbounds: %v", err)
	}
	if len(outbounds) != 3 {
		t.Errorf("outbounds = %d, want 3 (profile's own)", len(outbounds))
	}
	if _, ok := got["dns"]; !ok {
		t.Error("profile dns dropped")
	}

	// No catch-all rule: the profile's rules already route via its balancer, and
	// appending one would shadow them.
	rules, _ := routing["rules"].([]interface{})
	for _, r := range rules {
		m, _ := r.(map[string]interface{})
		if m["network"] == "tcp,udp" && m["outboundTag"] == "proxy" {
			t.Error("catch-all rule injected into a balancer profile")
		}
	}

	// Log level is ours.
	var logCfg LogConfig
	if err := json.Unmarshal(got["log"], &logCfg); err != nil {
		t.Fatalf("log: %v", err)
	}
	if logCfg.Loglevel != "warning" {
		t.Errorf("loglevel = %q, want warning", logCfg.Loglevel)
	}

	// remarks is metadata from the panel — xray rejects unknown top-level fields.
	if _, ok := got["remarks"]; ok {
		t.Error("remarks must be stripped before handing the config to xray")
	}
}

// Picking one server out of a profile must not throw the panel's routing away:
// the rules that send .ru domains and IP checkers straight out are exactly what
// the user loses otherwise.
func TestMergeProfileSingle_KeepsRoutingPinsOneOutbound(t *testing.T) {
	raw := json.RawMessage(`{
	  "remarks": "Auto",
	  "dns": {"servers": ["77.88.8.8"]},
	  "inbounds": [{"tag": "socks-from-panel", "port": 10999, "protocol": "socks"}],
	  "outbounds": [
	    {"tag": "proxy", "protocol": "vless"},
	    {"tag": "proxy-2", "protocol": "vless"},
	    {"tag": "direct", "protocol": "freedom"}
	  ],
	  "routing": {"balancers": [{"tag": "Balancer", "selector": ["proxy"]}],
	              "rules": [{"type": "field", "domain": ["2ip.ru"], "outboundTag": "direct"},
	                        {"type": "field", "balancerTag": "Balancer"}]},
	  "burstObservatory": {"subjectSelector": ["proxy"]}
	}`)

	inbounds := []Inbound{{Tag: "socks", Port: 10808, Protocol: "socks"}}
	cfg, err := MergeProfileSingle(raw, "proxy-2", inbounds, "warning")
	if err != nil {
		t.Fatalf("MergeProfileSingle: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	var routing map[string]interface{}
	if err := json.Unmarshal(got["routing"], &routing); err != nil {
		t.Fatalf("routing: %v", err)
	}
	rules, _ := routing["rules"].([]interface{})
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2 (the panel's own)", len(rules))
	}
	first, _ := rules[0].(map[string]interface{})
	if first["outboundTag"] != "direct" {
		t.Errorf("panel's direct rule lost: %+v", first)
	}
	// Without a balancer, balancerTag points at nothing — it must become the
	// chosen server's tag or the rule silently stops matching.
	second, _ := rules[1].(map[string]interface{})
	if _, ok := second["balancerTag"]; ok {
		t.Error("balancerTag kept while the balancer was dropped")
	}
	if second["outboundTag"] != "proxy-2" {
		t.Errorf("balancer rule outboundTag = %v, want proxy-2", second["outboundTag"])
	}
	if _, ok := routing["balancers"]; ok {
		t.Error("balancers kept for a single-server session")
	}
	if _, ok := got["burstObservatory"]; ok {
		t.Error("burstObservatory kept without a balancer to feed")
	}

	// The chosen outbound leads, so unmatched traffic defaults to it.
	var outbounds []map[string]interface{}
	if err := json.Unmarshal(got["outbounds"], &outbounds); err != nil {
		t.Fatalf("outbounds: %v", err)
	}
	if outbounds[0]["tag"] != "proxy-2" {
		t.Errorf("outbounds[0] = %v, want the chosen proxy-2", outbounds[0]["tag"])
	}
	if len(outbounds) != 3 {
		t.Errorf("outbounds = %d, want 3: rules may reference any of them", len(outbounds))
	}

	if _, ok := got["dns"]; !ok {
		t.Error("profile dns dropped — .ru names must keep resolving via the panel's resolver")
	}
	var gotInbounds []Inbound
	if err := json.Unmarshal(got["inbounds"], &gotInbounds); err != nil {
		t.Fatalf("inbounds: %v", err)
	}
	if len(gotInbounds) != 1 || gotInbounds[0].Port != 10808 {
		t.Errorf("inbounds not replaced: %+v", gotInbounds)
	}
	if _, ok := got["remarks"]; ok {
		t.Error("remarks must be stripped before handing the config to xray")
	}
}

// A URL-list subscription has no panel routing to keep; the caller must fall
// back to template.json rather than get a config with no rules at all.
func TestMergeProfileSingle_NoRoutingIsRejected(t *testing.T) {
	raw := json.RawMessage(`{"outbounds": [{"tag": "proxy", "protocol": "vless"}]}`)
	if _, err := MergeProfileSingle(raw, "proxy", nil, "warning"); !errors.Is(err, ErrNoPanelRouting) {
		t.Fatalf("err = %v, want ErrNoPanelRouting", err)
	}
}

func TestMergeProfileSingle_UnknownTagIsRejected(t *testing.T) {
	raw := json.RawMessage(`{
	  "outbounds": [{"tag": "proxy", "protocol": "vless"}],
	  "routing": {"rules": [{"type": "field", "balancerTag": "Balancer"}]}
	}`)
	if _, err := MergeProfileSingle(raw, "proxy-9", nil, "warning"); err == nil {
		t.Fatal("expected an error when the chosen tag is not in the profile")
	}
}

func TestMergeProfile_RejectsBrokenJSON(t *testing.T) {
	if _, err := MergeProfile(json.RawMessage(`{"outbounds":`), nil, "warning"); err == nil {
		t.Fatal("expected an error for malformed profile JSON")
	}
}
