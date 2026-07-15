package xraycfg

import (
	"encoding/json"
	"fmt"
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
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("profile config: %w", err)
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

	return json.Marshal(cfg)
}
