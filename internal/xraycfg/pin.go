package xraycfg

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// pinStrategy is the domainStrategy a pinned outbound dials with. IPv4 only:
// the Linux tunnel refuses IPv6, and the pinned addresses are IPv4.
const pinStrategy = "UseIPv4"

// PinServerAddresses makes the core reach its servers without the system
// resolver. Once the system's DNS goes into the tunnel, a server named by a
// domain would be looked up through the very tunnel that needs the server to
// work, and the uplink never comes up. So every server domain is resolved
// here, while DNS still takes the physical path, and written into the core's
// own dns.hosts; the proxy outbounds that dial a domain get a domainStrategy,
// which makes the core look the name up in its own DNS — hosts first — rather
// than in the system's.
//
// The panel's settings win: a hosts entry it already has for the domain, and a
// domainStrategy it already set, are left as they are. A domain that does not
// resolve is not pinned. The result maps each pinned domain to its addresses,
// for the route exceptions and the kill switch, which must name the same ones.
func PinServerAddresses(raw json.RawMessage, lookup func(host string) []string) (json.RawMessage, map[string][]string, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	outKey, err := soleField(cfg, "outbounds")
	if err != nil || outKey == "" {
		return raw, nil, err
	}

	pinned := map[string][]string{}
	for _, s := range OutboundServers(cfg[outKey]) {
		if net.ParseIP(s.Host) != nil {
			continue
		}
		if _, done := pinned[s.Host]; done {
			continue
		}
		if ips := lookup(s.Host); len(ips) > 0 {
			pinned[s.Host] = ips
		}
	}
	if len(pinned) == 0 {
		return raw, nil, nil
	}

	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(cfg[outKey], &outbounds); err != nil {
		return nil, nil, fmt.Errorf("outbounds: %w", err)
	}
	for i, ob := range outbounds {
		one, _ := json.Marshal([]map[string]json.RawMessage{ob})
		if !dialsPinned(one, pinned) {
			continue
		}
		streamKey, err := soleField(ob, "streamSettings")
		if err != nil {
			return nil, nil, fmt.Errorf("outbounds: %w", err)
		}
		if streamKey == "" {
			streamKey = "streamSettings"
		}
		stream, err := withStrategy(ob[streamKey])
		if err != nil {
			return nil, nil, err
		}
		ob[streamKey] = stream
		outbounds[i] = ob
	}
	if cfg[outKey], err = json.Marshal(outbounds); err != nil {
		return nil, nil, fmt.Errorf("outbounds: %w", err)
	}

	dnsKey, err := soleField(cfg, "dns")
	if err != nil {
		return nil, nil, err
	}
	if dnsKey == "" {
		dnsKey = "dns"
	}
	dns, err := withHosts(cfg[dnsKey], pinned)
	if err != nil {
		return nil, nil, err
	}
	cfg[dnsKey] = dns

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	return out, pinned, nil
}

// dialsPinned reports whether the one outbound in raw dials a pinned domain.
func dialsPinned(raw json.RawMessage, pinned map[string][]string) bool {
	for _, s := range OutboundServers(raw) {
		if _, ok := pinned[s.Host]; ok {
			return true
		}
	}
	return false
}

// withStrategy sets sockopt.domainStrategy unless the panel chose one.
func withStrategy(raw json.RawMessage) (json.RawMessage, error) {
	stream, err := object(raw, "streamSettings")
	if err != nil {
		return nil, err
	}
	sockoptKey, err := soleField(stream, "sockopt")
	if err != nil {
		return nil, fmt.Errorf("streamSettings: %w", err)
	}
	if sockoptKey == "" {
		sockoptKey = "sockopt"
	}
	sockopt, err := object(stream[sockoptKey], "sockopt")
	if err != nil {
		return nil, err
	}
	key, err := soleField(sockopt, "domainStrategy")
	if err != nil {
		return nil, fmt.Errorf("sockopt: %w", err)
	}
	if key != "" {
		return raw, nil
	}
	sockopt["domainStrategy"], _ = json.Marshal(pinStrategy)
	if stream[sockoptKey], err = json.Marshal(sockopt); err != nil {
		return nil, err
	}
	return json.Marshal(stream)
}

// withHosts adds a full-match hosts entry for each pinned domain the panel's
// dns does not map already.
func withHosts(raw json.RawMessage, pinned map[string][]string) (json.RawMessage, error) {
	dns, err := object(raw, "dns")
	if err != nil {
		return nil, err
	}
	hostsKey, err := soleField(dns, "hosts")
	if err != nil {
		return nil, fmt.Errorf("dns: %w", err)
	}
	if hostsKey == "" {
		hostsKey = "hosts"
	}
	hosts, err := object(dns[hostsKey], "dns.hosts")
	if err != nil {
		return nil, err
	}
	for domain, ips := range pinned {
		if hasHost(hosts, domain) {
			continue
		}
		hosts["full:"+domain], _ = json.Marshal(ips)
	}
	if dns[hostsKey], err = json.Marshal(hosts); err != nil {
		return nil, err
	}
	return json.Marshal(dns)
}

// hasHost reports whether hosts already maps exactly this domain, plainly or
// as a full match: the core reads both as the whole name.
func hasHost(hosts map[string]json.RawMessage, domain string) bool {
	for k := range hosts {
		k = strings.ToLower(k)
		if k == strings.ToLower(domain) || k == "full:"+strings.ToLower(domain) {
			return true
		}
	}
	return false
}

// object decodes a JSON object that may be absent or null into a map that is
// never nil.
func object(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}
