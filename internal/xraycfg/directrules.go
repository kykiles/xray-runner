package xraycfg

import "encoding/json"

// HasBypassRules reports whether the config routes anything past the proxy —
// "direct" and "block" both mean the tunnel does not carry that traffic.
//
// This exists so the status screen can stop claiming more than it does (A03).
// The client no longer hardcodes a bypass list, but a panel profile routinely
// ships one — .ru domains and the IP-checking sites among them — and
// MergeProfile keeps it on purpose. A screen reading "весь трафик через VPN"
// over such a profile tells the user something the config contradicts, and the
// user finds out by seeing their provider's address on an IP checker.
func HasBypassRules(raw json.RawMessage) bool {
	var cfg struct {
		Routing struct {
			Rules []struct {
				OutboundTag string `json:"outboundTag"`
				BalancerTag string `json:"balancerTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false
	}
	for _, r := range cfg.Routing.Rules {
		// A rule aimed at a balancer goes through the proxy outbounds it groups,
		// so it is not a bypass whatever the balancer is called.
		if r.BalancerTag != "" {
			continue
		}
		switch r.OutboundTag {
		case "direct", "block", "blocked":
			return true
		}
	}
	return false
}
