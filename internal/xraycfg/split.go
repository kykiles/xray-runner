package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Split routing: the whole system's traffic arrives through the TUN inbound and
// xray decides per connection where it goes, matching the owning process by
// name. Xray looks the process up itself (routing rule "process"), so a program
// started after the session is up needs nothing — there is no list to refresh.
//
// The rule cannot be inverted: xray has no "every process except these", so the
// order of the rules is what makes the mode safe. See ADR-0003.

// ErrNoTunnelTarget means the config has nothing the listed processes could be
// sent to: no rule leading into the tunnel, and no server as the first outbound.
var ErrNoTunnelTarget = errors.New("в конфиге не найден outbound туннеля")

// ApplySplitRouting rewrites a finished config so that only the named processes
// travel the tunnel and everything else goes out directly.
//
// The panel's rules stay where they were, in their order. Those leading
// somewhere local — a freedom, blackhole or dns outbound, whatever its tag —
// are kept as they are. Those leading into the tunnel are narrowed to the
// listed processes: left as they were, the panel's catch-all would send the
// whole system through the tunnel no matter which process opened the
// connection. After them the listed processes go where unmatched traffic goes —
// the first outbound — and everything else goes direct. So a listed process
// goes exactly where a TUN session would have sent it, balancer by balancer
// and domain by domain.
func ApplySplitRouting(raw json.RawMessage, processes []string) (json.RawMessage, error) {
	if len(processes) == 0 {
		return nil, errors.New("список процессов пуст")
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	outbounds, err := readOutbounds(cfg["outbounds"])
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	directTag := ""
	for _, o := range outbounds {
		if o.Tag == "" || !isLocalProtocol(o.Protocol) {
			continue
		}
		local[o.Tag] = true
		if o.Protocol == "freedom" && directTag == "" {
			directTag = o.Tag
		}
	}
	// Unmatched traffic takes the first outbound. When that is the tunnel, the
	// listed processes are sent there once the panel's rules are through.
	defaultTag := ""
	if len(outbounds) > 0 && outbounds[0].Tag != "" && !local[outbounds[0].Tag] {
		defaultTag = outbounds[0].Tag
	}
	if directTag == "" {
		// A panel without a freedom outbound has no way out except the tunnel,
		// which is the one thing the mode must not do with unlisted traffic.
		withDirect, err := appendDirectOutbound(cfg["outbounds"])
		if err != nil {
			return nil, err
		}
		cfg["outbounds"] = withDirect
		directTag = directOutboundTag
		local[directTag] = true
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

	out := make([]map[string]json.RawMessage, 0, len(rules)+2)
	tunnel := false
	for _, r := range rules {
		if len(r["balancerTag"]) == 0 {
			tag, err := ruleOutboundTag(r)
			if err != nil {
				return nil, err
			}
			if local[tag] {
				out = append(out, r)
				continue
			}
			// A rule with no tag at all rides the default outbound: there is
			// nothing to narrow, and keeping it would emit a rule xray rejects.
			if tag == "" {
				continue
			}
		}
		narrowed, ok, err := forProcesses(r, processes)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, narrowed)
		tunnel = true
	}
	if defaultTag != "" {
		tag, err := json.Marshal(defaultTag)
		if err != nil {
			return nil, err
		}
		rest, err := splitRule(processes, "outboundTag", tag)
		if err != nil {
			return nil, err
		}
		out = append(out, rest)
		tunnel = true
	}
	if !tunnel {
		return nil, ErrNoTunnelTarget
	}
	catchAll, err := catchAllRule(directTag)
	if err != nil {
		return nil, err
	}

	if routing["rules"], err = json.Marshal(append(out, catchAll)); err != nil {
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

// outboundInfo is what the routing rewrites need to know of an outbound.
type outboundInfo struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
}

func readOutbounds(raw json.RawMessage) ([]outboundInfo, error) {
	var outbounds []outboundInfo
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, fmt.Errorf("outbounds: %w", err)
		}
	}
	return outbounds, nil
}

// isLocalProtocol reports whether an outbound of this protocol resolves
// traffic without the tunnel: freedom (direct), blackhole (blocked) and dns
// (answered by xray itself).
func isLocalProtocol(protocol string) bool {
	switch protocol {
	case "freedom", "blackhole", "dns":
		return true
	}
	return false
}

// forProcesses narrows a tunnel rule to the listed processes. A rule that names
// processes of its own keeps those of them that are listed; ok is false when
// none are, and the rule is dropped.
func forProcesses(r map[string]json.RawMessage, processes []string) (map[string]json.RawMessage, bool, error) {
	names := processes
	if len(r["process"]) > 0 {
		own, err := stringList(r["process"])
		if err != nil {
			return nil, false, fmt.Errorf("routing rule process: %w", err)
		}
		names = nil
		for _, p := range own {
			if slices.ContainsFunc(processes, func(l string) bool { return strings.EqualFold(l, strings.TrimSpace(p)) }) {
				names = append(names, strings.TrimSpace(p))
			}
		}
		if len(names) == 0 {
			return nil, false, nil
		}
	}
	procs, err := json.Marshal(names)
	if err != nil {
		return nil, false, err
	}
	narrowed := maps.Clone(r)
	narrowed["process"] = procs
	return narrowed, true, nil
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
