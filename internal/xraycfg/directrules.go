package xraycfg

import "encoding/json"

// HasBypassRules reports whether the config routes anything past the proxy —
// to a freedom (direct) or blackhole (block) outbound, whatever its tag.
//
// This exists so the status screen can stop claiming more than it does (A03).
// The client no longer hardcodes a bypass list, but a panel profile routinely
// ships one — .ru domains and the IP-checking sites among them — and
// MergeProfile keeps it on purpose. A screen reading "весь трафик через VPN"
// over such a profile tells the user something the config contradicts, and the
// user finds out by seeing their provider's address on an IP checker.
//
// A tag is judged by the protocol of the outbound it names; the names "direct",
// "block" and "blocked" count only when no outbound in the config carries them.
// A first outbound that is itself direct or blocking counts too: everything no
// rule matches goes there.
func HasBypassRules(raw json.RawMessage) bool {
	var cfg struct {
		Outbounds []outboundInfo `json:"outbounds"`
		Routing   struct {
			Rules []struct {
				OutboundTag string `json:"outboundTag"`
				BalancerTag string `json:"balancerTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false
	}
	protocols := map[string]string{}
	for _, o := range cfg.Outbounds {
		if _, seen := protocols[o.Tag]; !seen {
			protocols[o.Tag] = o.Protocol
		}
	}
	if len(cfg.Outbounds) > 0 && bypassProtocol(cfg.Outbounds[0].Protocol) {
		return true
	}
	for _, r := range cfg.Routing.Rules {
		// A rule aimed at a balancer goes through the proxy outbounds it groups,
		// so it is not a bypass whatever the balancer is called.
		if r.BalancerTag != "" || r.OutboundTag == "" {
			continue
		}
		if p, declared := protocols[r.OutboundTag]; declared {
			if bypassProtocol(p) {
				return true
			}
			continue
		}
		switch r.OutboundTag {
		case "direct", "block", "blocked":
			return true
		}
	}
	return false
}

// bypassProtocol reports whether an outbound of this protocol takes traffic off
// the tunnel: freedom sends it out directly, blackhole drops it.
func bypassProtocol(protocol string) bool {
	return protocol == "freedom" || protocol == "blackhole"
}
