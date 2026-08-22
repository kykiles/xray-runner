package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Split routing: the whole system's traffic arrives through the TUN inbound and
// xray decides per connection where it goes, matching the owning process by
// name. Xray looks the process up itself (routing rule "process"), so a program
// started after the session is up needs nothing — there is no list to refresh.
//
// The rule cannot be inverted: xray has no "every process except these", so the
// order of the rules is what makes the mode safe. See ADR-0003.

// ErrNoTunnelTarget means the config has nothing the split rule could point at:
// no balancer and no rule leading into a proxy outbound.
var ErrNoTunnelTarget = errors.New("в конфиге не найден outbound туннеля")

// ApplySplitRouting rewrites a finished config so that only the named processes
// travel the tunnel and everything else goes out directly.
//
// The panel's own rules are kept only where they lead somewhere local — direct,
// block, the dns outbound. Rules leading into the tunnel are dropped: the panel
// ends with a catch-all of its own, and left in place it would send the whole
// system through the tunnel no matter which process opened the connection.
// Their target is what the process rule then aims at, so the listed processes
// go exactly where a TUN session would have sent everything.
func ApplySplitRouting(raw json.RawMessage, processes []string) (json.RawMessage, error) {
	if len(processes) == 0 {
		return nil, errors.New("список процессов пуст")
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	local, err := localOutbounds(cfg["outbounds"])
	if err != nil {
		return nil, err
	}
	directTag := local["freedom"]
	if directTag == "" {
		// A panel without a freedom outbound has no way out except the tunnel,
		// which is the one thing the mode must not do with unlisted traffic.
		outbounds, err := appendDirectOutbound(cfg["outbounds"])
		if err != nil {
			return nil, err
		}
		cfg["outbounds"] = outbounds
		directTag = directOutboundTag
		local["freedom"] = directTag
	}

	routing := map[string]json.RawMessage{}
	if len(cfg["routing"]) > 0 {
		if err := json.Unmarshal(cfg["routing"], &routing); err != nil {
			return nil, fmt.Errorf("routing: %w", err)
		}
	}
	var rules []map[string]json.RawMessage
	if len(routing["rules"]) > 0 {
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			return nil, fmt.Errorf("routing rules: %w", err)
		}
	}

	kept := make([]map[string]json.RawMessage, 0, len(rules))
	var tunnelKey string
	var tunnelTag json.RawMessage
	for _, r := range rules {
		if b := r["balancerTag"]; len(b) > 0 {
			tunnelKey, tunnelTag = "balancerTag", b
			continue
		}
		tag, err := ruleOutboundTag(r)
		if err != nil {
			return nil, err
		}
		if isLocalTag(local, tag) {
			kept = append(kept, r)
			continue
		}
		// A rule with no tag at all rides the default outbound: there is nothing
		// to point the split rule at, and taking it anyway emits
		// "outboundTag": null, which xray rejects outright.
		if out := r["outboundTag"]; len(out) > 0 {
			tunnelKey, tunnelTag = "outboundTag", out
		}
	}
	if tunnelKey == "" {
		return nil, ErrNoTunnelTarget
	}

	split, err := splitRule(processes, tunnelKey, tunnelTag)
	if err != nil {
		return nil, err
	}
	catchAll, err := catchAllRule(directTag)
	if err != nil {
		return nil, err
	}

	if routing["rules"], err = json.Marshal(append(kept, split, catchAll)); err != nil {
		return nil, fmt.Errorf("routing rules: %w", err)
	}
	if cfg["routing"], err = json.Marshal(routing); err != nil {
		return nil, fmt.Errorf("routing: %w", err)
	}
	return json.Marshal(cfg)
}

// directOutboundTag is the tag of the freedom outbound added to a config that
// has none.
const directOutboundTag = "direct"

// localOutbounds maps each protocol that resolves traffic without the tunnel to
// the tag of the first outbound carrying it: freedom (direct), blackhole
// (blocked) and dns (answered by xray itself).
func localOutbounds(raw json.RawMessage) (map[string]string, error) {
	var outbounds []struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, fmt.Errorf("outbounds: %w", err)
		}
	}
	local := map[string]string{}
	for _, o := range outbounds {
		switch o.Protocol {
		case "freedom", "blackhole", "dns":
			if local[o.Protocol] == "" {
				local[o.Protocol] = o.Tag
			}
		}
	}
	return local, nil
}

// isLocalTag reports whether a rule's outboundTag names one of the outbounds
// that never enter the tunnel. An empty tag (a rule pointing nowhere) counts as
// tunnel-bound: it falls through to the first outbound, which is the proxy.
func isLocalTag(local map[string]string, tag string) bool {
	if tag == "" {
		return false
	}
	for _, t := range local {
		if t == tag {
			return true
		}
	}
	return false
}

func ruleOutboundTag(r map[string]json.RawMessage) (string, error) {
	if len(r["outboundTag"]) == 0 {
		return "", nil
	}
	var tag string
	if err := json.Unmarshal(r["outboundTag"], &tag); err != nil {
		return "", fmt.Errorf("routing rule outboundTag: %w", err)
	}
	return tag, nil
}

func appendDirectOutbound(raw json.RawMessage) (json.RawMessage, error) {
	var outbounds []json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, fmt.Errorf("outbounds: %w", err)
		}
	}
	direct, err := json.Marshal(map[string]string{"tag": directOutboundTag, "protocol": "freedom"})
	if err != nil {
		return nil, err
	}
	return json.Marshal(append(outbounds, direct))
}

func splitRule(processes []string, key string, tag json.RawMessage) (map[string]json.RawMessage, error) {
	procs, err := json.Marshal(processes)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{
		"type":    json.RawMessage(`"field"`),
		"process": procs,
		key:       tag,
	}, nil
}

func catchAllRule(directTag string) (map[string]json.RawMessage, error) {
	tag, err := json.Marshal(directTag)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{
		"type":        json.RawMessage(`"field"`),
		"network":     json.RawMessage(`"tcp,udp"`),
		"outboundTag": tag,
	}, nil
}
