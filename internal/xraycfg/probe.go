package xraycfg

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"strings"
)

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
// outbound through a server — unmatched traffic falls through to the first
// outbound, and a panel that puts direct first still sends its traffic to a
// server by rules. Either way the probe measures the tunnel.
// hosts carries two kinds of entry: a domain as "full:<domain>", and an IP
// target as a bare literal. The two cannot share one rule — a rule's domain and
// ip fields are alternatives the request has to satisfy together, and a request
// carries a name or an address, never both — so an IP target gets a rule of its
// own aimed at the same place (F04).
func PrependProbeRule(raw json.RawMessage, hosts []string) (json.RawMessage, error) {
	if len(hosts) == 0 {
		return raw, nil
	}
	domains, ips, err := splitProbeHosts(hosts)
	if err != nil {
		return nil, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}

	aim := map[string]any{}
	switch {
	case firstTag(cfg["routing"], "balancers") != "":
		aim["balancerTag"] = firstTag(cfg["routing"], "balancers")
	case firstTunnelOutboundTag(cfg["outbounds"]) != "":
		aim["outboundTag"] = firstTunnelOutboundTag(cfg["outbounds"])
	default:
		// Nothing to aim at — leave the config as it is rather than write a rule
		// pointing at a tag that does not exist, which xray refuses to start on.
		return raw, nil
	}

	// One rule per kind, and none at all for a kind with nothing in it: a rule
	// carrying an empty domain or ip list matches everything, which would turn
	// the probe rule into a catch-all over the profile's own routing.
	var probeRules []json.RawMessage
	for _, kind := range []struct {
		field  string
		values []string
	}{{"domain", domains}, {"ip", ips}} {
		if len(kind.values) == 0 {
			continue
		}
		rule := map[string]any{"type": "field", kind.field: kind.values}
		maps.Copy(rule, aim)
		ruleJSON, err := json.Marshal(rule)
		if err != nil {
			return nil, err
		}
		probeRules = append(probeRules, ruleJSON)
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
	routing["rules"], err = json.Marshal(append(probeRules, rules...))
	if err != nil {
		return nil, err
	}
	if cfg["routing"], err = json.Marshal(routing); err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// splitProbeHosts sorts the probe targets into domain matchers and exact IP
// networks — /32 for IPv4, /128 for IPv6 — so each kind can be given a rule the
// core will actually match.
func splitProbeHosts(hosts []string) (domains, ips []string, err error) {
	for _, h := range hosts {
		if strings.HasPrefix(h, "full:") {
			domains = append(domains, h)
			continue
		}
		addr, parseErr := netip.ParseAddr(h)
		if parseErr != nil {
			domains = append(domains, h)
			continue
		}
		if addr.Zone() != "" {
			// A zone names one of this host's interfaces and means nothing to a
			// routing rule. Dropping it quietly would leave the probe matching
			// nothing at all, which is the failure this whole rule exists to
			// prevent, so say so instead.
			return nil, nil, fmt.Errorf("проверочный адрес %q содержит зону интерфейса (%%%s) — такой адрес нельзя направить правилом маршрутизации, уберите зону из HEALTH_CHECK_URL", h, addr.Zone())
		}
		ips = append(ips, netip.PrefixFrom(addr, addr.BitLen()).String())
	}
	return domains, ips, nil
}

// ProbeInboundTag names the loopback inbound a tun session's probes go through.
const ProbeInboundTag = "probe"

// BuildProbeInbound is the HTTP listener a tun session's probes use (A10). A tun
// session has no local proxy of its own, and a probe sent down the system
// routes follows whatever they and the profile's direct rules say — a wrong
// route or a direct rule for the probe host reads "ок" in front of a dead
// tunnel. Through this inbound the probe meets PrependProbeRule and takes the
// server's outbound, the same as the proxy-mode check.
func BuildProbeInbound(port int) Inbound {
	return Inbound{
		Tag:      ProbeInboundTag,
		Port:     port,
		Listen:   "127.0.0.1",
		Protocol: "http",
		Settings: json.RawMessage(`{"allowTransparent":false}`),
	}
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

// firstTunnelOutboundTag reads the tag of the first outbound that goes through
// a server: a freedom, blackhole or dns outbound put first by the panel is not
// the tunnel, and a probe aimed at it would read "ок" off the plain internet.
func firstTunnelOutboundTag(arr json.RawMessage) string {
	outbounds, err := readOutbounds(arr)
	if err != nil {
		return ""
	}
	for _, o := range outbounds {
		if o.Tag != "" && !isLocalProtocol(o.Protocol) {
			return o.Tag
		}
	}
	return ""
}
