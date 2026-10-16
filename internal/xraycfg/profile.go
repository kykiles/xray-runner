package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// panelOnlyFields are metadata the panel adds to a profile config; xray does not
// understand them, so they are stripped before launch.
var panelOnlyFields = []string{"remarks", "policy", "stats", "api"}

// MergeProfile prepares a subscription profile to run as-is. The profile brings
// its own outbounds, balancers and routing — that grouping is the whole point of
// the profile — so only the inbounds (which target another client) and the log
// level are replaced. Unlike MergeConfig, no catch-all rule is added: the
// profile's rules already point at its balancer, and a catch-all would shadow
// them.
func MergeProfile(raw json.RawMessage, inbounds []Inbound, logLevel string) (json.RawMessage, error) {
	cfg, err := profileBase(raw, inbounds, logLevel)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// ErrNoPanelRouting means the profile carries no routing of its own, so there is
// nothing to preserve and the caller should build from template.json instead.
var ErrNoPanelRouting = errors.New("profile has no routing of its own")

// MergeProfileSingle keeps the panel's routing and dns while pinning the session
// to one server. Selecting a server used to rebuild the config from
// template.json, which silently dropped the panel's rules — the ones sending
// .ru domains and IP checkers out directly. The balancer cannot survive a single
// server, so it is removed and every rule aiming at it is re-pointed at the
// chosen outbound. All outbounds are kept because a rule may name any of them;
// the chosen one is moved first so unmatched traffic defaults to it.
func MergeProfileSingle(raw json.RawMessage, outboundTag string, inbounds []Inbound, logLevel string) (json.RawMessage, error) {
	cfg, err := profileBase(raw, inbounds, logLevel)
	if err != nil {
		return nil, err
	}
	if len(cfg["routing"]) == 0 {
		return nil, ErrNoPanelRouting
	}
	// The rules name outbounds by tag: a server without one cannot be pinned
	// under them, and the first untagged outbound is not necessarily it.
	if outboundTag == "" {
		return nil, errors.New("у выбранного сервера профиля нет tag — правила профиля к нему не привязать")
	}

	outbounds, err := pinOutbound(cfg["outbounds"], outboundTag)
	if err != nil {
		return nil, err
	}
	cfg["outbounds"] = outbounds

	routing, err := dropBalancers(cfg["routing"], outboundTag)
	if err != nil {
		return nil, err
	}
	cfg["routing"] = routing
	delete(cfg, "burstObservatory")
	delete(cfg, "observatory")

	return json.Marshal(cfg)
}

// profileBase does what both merges share: our inbounds, our log level, no panel
// metadata.
func profileBase(raw json.RawMessage, inbounds []Inbound, logLevel string) (map[string]json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("profile config: %w", err)
	}
	if err := canonicalTopKeys(cfg); err != nil {
		return nil, err
	}
	if len(cfg["outbounds"]) == 0 {
		return nil, fmt.Errorf("profile config has no outbounds")
	}

	inb, err := json.Marshal(inbounds)
	if err != nil {
		return nil, fmt.Errorf("inbounds: %w", err)
	}
	cfg["inbounds"] = inb

	logCfg, err := json.Marshal(LogConfig{Loglevel: logLevel})
	if err != nil {
		return nil, fmt.Errorf("log config: %w", err)
	}
	cfg["log"] = logCfg

	for _, f := range panelOnlyFields {
		delete(cfg, f)
	}

	return cfg, nil
}

// canonicalTopKeys renames the top-level keys the code below addresses by exact
// name. The core reads keys without regard to case, so "Log" is its log section
// just as much as "log" is; left alone, such a key would slip past the
// replacements and deletions that follow. Two keys that the core would read as
// one field are refused, since which of them wins is not ours to guess.
func canonicalTopKeys(cfg map[string]json.RawMessage) error {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		f := foldKey(k)
		if seen[f] {
			return fmt.Errorf("profile config: ключ %q повторяется (с точностью до регистра)", k)
		}
		seen[f] = true
	}

	names := append([]string{"log", "inbounds", "outbounds", "routing", "observatory", "burstObservatory"}, panelOnlyFields...)
	for _, k := range keys {
		for _, name := range names {
			if k != name && sameKey(k, name) {
				cfg[name] = cfg[k]
				delete(cfg, k)
				break
			}
		}
	}
	return nil
}

// OutboundTag reads an outbound's tag. Empty when it has none or does not parse.
func OutboundTag(raw json.RawMessage) string {
	var head struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ""
	}
	return head.Tag
}

// pinOutbound moves the chosen outbound to the front, making it the default for
// traffic no rule matches.
func pinOutbound(raw json.RawMessage, tag string) (json.RawMessage, error) {
	var outbounds []json.RawMessage
	if err := json.Unmarshal(raw, &outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}

	idx := -1
	for i, o := range outbounds {
		if OutboundTag(o) == tag {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("outbound %q not found in profile", tag)
	}

	chosen := outbounds[idx]
	rest := append(outbounds[:idx:idx], outbounds[idx+1:]...)
	return json.Marshal(append([]json.RawMessage{chosen}, rest...))
}

// dropBalancers removes the balancer definitions and re-points the rules that
// used them at the chosen outbound.
func dropBalancers(raw json.RawMessage, tag string) (json.RawMessage, error) {
	var routing map[string]json.RawMessage
	if err := json.Unmarshal(raw, &routing); err != nil {
		return nil, fmt.Errorf("routing: %w", err)
	}
	delete(routing, "balancers")

	if len(routing["rules"]) > 0 {
		var rules []map[string]json.RawMessage
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			return nil, fmt.Errorf("routing rules: %w", err)
		}
		chosen, err := json.Marshal(tag)
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			if _, ok := r["balancerTag"]; !ok {
				continue
			}
			delete(r, "balancerTag")
			r["outboundTag"] = chosen
		}
		encoded, err := json.Marshal(rules)
		if err != nil {
			return nil, fmt.Errorf("routing rules: %w", err)
		}
		routing["rules"] = encoded
	}

	return json.Marshal(routing)
}
