package xraycfg

import "encoding/json"

// PrependProbeRule routes the connectivity-probe hosts down the path under test,
// ahead of every rule the panel brought with it.
//
// Panels routinely send the captive-portal checkers (google generate_204 and
// friends) out `direct`, or block them outright. Left alone, the probe then
// measures the plain internet instead of the tunnel: the ping column reads
// "timeout" for a server that connects fine, and the status screen reads "ок"
// while nothing at all is going through the proxy.
//
// The target is read out of the config rather than passed in: a profile's
// balancer is where its traffic actually goes, and without one the first
// outbound is what unmatched traffic falls through to. Either way the probe
// follows the same path as the user's own traffic.
func PrependProbeRule(raw json.RawMessage, hosts []string) (json.RawMessage, error) {
	if len(hosts) == 0 {
		return raw, nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}

	rule := map[string]any{"type": "field", "domain": hosts}
	switch {
	case firstTag(cfg["routing"], "balancers") != "":
		rule["balancerTag"] = firstTag(cfg["routing"], "balancers")
	case firstOutboundTag(cfg["outbounds"]) != "":
		rule["outboundTag"] = firstOutboundTag(cfg["outbounds"])
	default:
		// Nothing to aim at — leave the config as it is rather than write a rule
		// pointing at a tag that does not exist, which xray refuses to start on.
		return raw, nil
	}
	ruleJSON, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}

	routing := map[string]json.RawMessage{}
	if len(cfg["routing"]) > 0 {
		if err := json.Unmarshal(cfg["routing"], &routing); err != nil {
			return nil, err
		}
	}
	var rules []json.RawMessage
	if len(routing["rules"]) > 0 {
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			return nil, err
		}
	}
	routing["rules"], err = json.Marshal(append([]json.RawMessage{ruleJSON}, rules...))
	if err != nil {
		return nil, err
	}
	if cfg["routing"], err = json.Marshal(routing); err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// firstTag reads the tag of the first element of a named array inside routing.
func firstTag(routing json.RawMessage, field string) string {
	if len(routing) == 0 {
		return ""
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(routing, &r) != nil {
		return ""
	}
	return firstOutboundTag(r[field])
}

// firstOutboundTag reads the tag of the first element of a JSON array of
// tagged objects (outbounds, balancers).
func firstOutboundTag(arr json.RawMessage) string {
	if len(arr) == 0 {
		return ""
	}
	var items []struct {
		Tag string `json:"tag"`
	}
	if json.Unmarshal(arr, &items) != nil || len(items) == 0 {
		return ""
	}
	return items[0].Tag
}
