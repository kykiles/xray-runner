package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ApplySecurityPolicy enforces the local ALLOW_INSECURE choice on a finished
// config (A01). A link is built with the subscription's insecure flag behind
// that opt-in, but a panel profile or a preserved outbound runs close to
// verbatim, and its own tlsSettings.allowInsecure used to reach the core past
// ALLOW_INSECURE=false. Applied once to whatever a builder produced, the policy
// holds whichever path the config took.
//
// Without the opt-in the flag is removed from every outbound — each link of a
// dialerProxy chain is an outbound of its own. With it, a flag the config
// already carries stays and none is added. Only
// outbounds[*].streamSettings.tlsSettings is read: a same-named field anywhere
// else belongs to something else. A value that is not a JSON boolean is refused
// rather than guessed at. The input is never modified, and when there is
// nothing to remove it comes back as is.
func ApplySecurityPolicy(raw json.RawMessage, allowInsecure bool) (json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(cfg["outbounds"]) == 0 {
		return raw, nil
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(cfg["outbounds"], &outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}

	changed := false
	for _, ob := range outbounds {
		cleared, err := clearInsecure(ob, allowInsecure)
		if err != nil {
			return nil, fmt.Errorf("outbound %s: %w", ob["tag"], err)
		}
		changed = changed || cleared
	}
	if !changed {
		return raw, nil
	}

	var err error
	if cfg["outbounds"], err = json.Marshal(outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}
	return json.Marshal(cfg)
}

// clearInsecure applies the policy to one outbound and reports whether it
// removed the flag.
func clearInsecure(ob map[string]json.RawMessage, allowInsecure bool) (bool, error) {
	if len(ob["streamSettings"]) == 0 {
		return false, nil
	}
	var stream map[string]json.RawMessage
	if err := json.Unmarshal(ob["streamSettings"], &stream); err != nil {
		return false, fmt.Errorf("streamSettings: %w", err)
	}
	if len(stream["tlsSettings"]) == 0 {
		return false, nil
	}
	var tls map[string]json.RawMessage
	if err := json.Unmarshal(stream["tlsSettings"], &tls); err != nil {
		return false, fmt.Errorf("tlsSettings: %w", err)
	}
	v, ok := tls["allowInsecure"]
	if !ok {
		return false, nil
	}
	var flag *bool
	if err := json.Unmarshal(v, &flag); err != nil || flag == nil {
		return false, errors.New("tlsSettings.allowInsecure is not true or false")
	}
	if allowInsecure {
		return false, nil
	}

	delete(tls, "allowInsecure")
	var err error
	if stream["tlsSettings"], err = json.Marshal(tls); err != nil {
		return false, err
	}
	if ob["streamSettings"], err = json.Marshal(stream); err != nil {
		return false, err
	}
	return true, nil
}
