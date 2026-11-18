package xraycfg

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// TunInboundTag tags the tun inbound; the DNS hijack rule matches on it.
const TunInboundTag = "tun"

// WindowsTunDNSAddr is a synthetic resolver reached through the TUN. A
// synthetic endpoint keeps Windows from upgrading a public resolver to DoH
// on its own and bypassing the panel's DNS configuration.
const WindowsTunDNSAddr = "192.0.2.53"

// The pieces HijackTunDNS adds. The dns outbound answers A and AAAA from the
// core's DNS and sends every other query on through the loopback, which hands
// it back to routing under its own inbound tag: there the panel's rules take it
// like any other traffic, balancer included, and the hijack rule, which matches
// the tun inbound only, cannot catch it a second time.
const (
	TunDNSOutboundTag = "tun-dns"
	tunDNSLoopTag     = "tun-dns-loop"
	tunDNSForwardTag  = "tun-dns-fwd"
)

// HijackTunDNS makes the core answer the system's DNS that comes through the
// tunnel from the panel's own dns servers (DoH as a rule), not from whatever
// plain resolver the tun link names. Without it the dns section reaches only
// the core's own lookups, and every name the system asks for still goes out as
// plain UDP.
//
// The core can only do it when its DNS never asks the system: the system's DNS
// now comes back into the core, so a lookup that leaves through the system
// returns to the very DNS that made it. That rules out a config with no dns
// servers of its own (the core then uses the system resolver), "localhost",
// "fakedns" (the tun inbound does not sniff fake addresses back) and every
// "+local" server, which dials past routing. Such a config is returned as it
// was, with the reason; the tunnel's DNS then still flows, unhijacked.
//
// Two more paths would reach the system resolver, and are closed here:
//   - a dns server named by a domain is pinned into dns.hosts, resolved now,
//     while the lookup still takes the physical path. The panel's own entry
//     for the domain wins; a domain that does not resolve leaves the config
//     unhijacked;
//   - freedom resolves a domain destination — the DoH request of a server the
//     panel routes direct — through the system unless told otherwise, ignoring
//     dns.hosts. A freedom outbound without a strategy of its own gets
//     sockopt.domainStrategy, which sends it to the core's DNS, hosts first.
func HijackTunDNS(raw json.RawMessage, lookup func(host string) []string) (json.RawMessage, string, error) {
	return hijackTunDNS(raw, lookup, "")
}

// HijackWindowsTunDNS also forwards the queries the core does not answer to
// 1.1.1.1 through the profile. The synthetic address is never dialled.
func HijackWindowsTunDNS(raw json.RawMessage, lookup func(host string) []string) (json.RawMessage, string, error) {
	out, skipped, err := hijackTunDNS(raw, lookup, WindowsTunDNSAddr)
	if err != nil || skipped == "" {
		return out, skipped, err
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, "", err
	}
	key, outbounds, err := readTunDNSOutbounds(cfg)
	if err != nil {
		return nil, "", err
	}
	out, err = finishTunDNS(cfg, key, outbounds, WindowsTunDNSAddr, false)
	return out, skipped, err
}

func hijackTunDNS(raw json.RawMessage, lookup func(host string) []string, ingress string) (out json.RawMessage, skipped string, err error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, "", fmt.Errorf("config: %w", err)
	}

	dnsKey, err := soleField(cfg, "dns")
	if err != nil {
		return nil, "", err
	}
	if dnsKey == "" {
		dnsKey = "dns"
	}
	dns, err := object(cfg[dnsKey], "dns")
	if err != nil {
		return nil, "", err
	}
	addrs, err := dnsServerAddresses(dns)
	if err != nil {
		return nil, "", err
	}
	if len(addrs) == 0 {
		return raw, "в конфиге нет своих DNS-серверов", nil
	}
	hosts := map[string][]string{}
	for _, a := range addrs {
		if asksSystem(a) {
			return raw, fmt.Sprintf("DNS-сервер %q спрашивает систему", a), nil
		}
		host := dnsServerHost(a)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		if _, done := hosts[host]; done {
			continue
		}
		hosts[host] = nil
	}

	outKey, outbounds, err := readTunDNSOutbounds(cfg)
	if err != nil {
		return nil, "", err
	}
	if outKey == "" {
		return raw, "в конфиге нет outbounds", nil
	}
	if tag := takenTag(outbounds, cfg); tag != "" {
		return raw, fmt.Sprintf("тег %q уже занят в конфиге", tag), nil
	}

	// The panel's hosts are read before any lookup: a domain it maps already
	// needs neither the lookup nor a pin.
	hostsKey, err := soleField(dns, "hosts")
	if err != nil {
		return nil, "", fmt.Errorf("dns: %w", err)
	}
	panelHosts, err := object(dns[hostsKey], "dns.hosts")
	if err != nil {
		return nil, "", err
	}
	pin := map[string][]string{}
	for host := range hosts {
		if hasHost(panelHosts, host) {
			continue
		}
		ips := lookup(host)
		if len(ips) == 0 {
			return raw, fmt.Sprintf("адрес DNS-сервера %s не найден", host), nil
		}
		pin[host] = ips
	}
	if len(pin) > 0 {
		if cfg[dnsKey], err = withHosts(cfg[dnsKey], pin); err != nil {
			return nil, "", err
		}
	}

	for i, ob := range outbounds {
		if !isFreedom(ob) || freedomHasStrategy(ob) {
			continue
		}
		streamKey, err := soleField(ob, "streamSettings")
		if err != nil {
			return nil, "", fmt.Errorf("outbounds: %w", err)
		}
		if streamKey == "" {
			streamKey = "streamSettings"
		}
		if ob[streamKey], err = withStrategy(ob[streamKey], pinStrategy); err != nil {
			return nil, "", err
		}
		outbounds[i] = ob
	}
	out, err = finishTunDNS(cfg, outKey, outbounds, ingress, true)
	return out, "", err
}

func readTunDNSOutbounds(cfg map[string]json.RawMessage) (string, []map[string]json.RawMessage, error) {
	key, err := soleField(cfg, "outbounds")
	if err != nil || key == "" {
		return key, nil, err
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(cfg[key], &outbounds); err != nil {
		return "", nil, fmt.Errorf("outbounds: %w", err)
	}
	return key, outbounds, nil
}

func finishTunDNS(cfg map[string]json.RawMessage, outKey string, outbounds []map[string]json.RawMessage, ingress string, hijack bool) (json.RawMessage, error) {
	if outKey == "" || len(outbounds) == 0 {
		return nil, fmt.Errorf("в конфиге нет outbounds для DNS туннеля")
	}
	if tag := takenTag(outbounds, cfg); tag != "" {
		return nil, fmt.Errorf("тег %q уже занят в конфиге", tag)
	}
	added, err := tunDNSOutbounds(ingress, hijack)
	if err != nil {
		return nil, err
	}
	// The first outbound remains the panel's default.
	cfg[outKey], err = json.Marshal(append(outbounds, added...))
	if err != nil {
		return nil, err
	}
	if err := addTunDNSRule(cfg, ingress); err != nil {
		return nil, err
	}
	return json.Marshal(cfg)
}

// dnsServerAddresses lists the address of every server in dns.servers, the
// short string form and the object form alike.
func dnsServerAddresses(dns map[string]json.RawMessage) ([]string, error) {
	key, err := soleField(dns, "servers")
	if err != nil {
		return nil, fmt.Errorf("dns: %w", err)
	}
	if key == "" || len(dns[key]) == 0 || string(dns[key]) == "null" {
		return nil, nil
	}
	var servers []json.RawMessage
	if err := json.Unmarshal(dns[key], &servers); err != nil {
		return nil, fmt.Errorf("dns.servers: %w", err)
	}
	var out []string
	for _, s := range servers {
		var plain string
		if json.Unmarshal(s, &plain) == nil {
			out = append(out, plain)
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(s, &obj); err != nil {
			return nil, fmt.Errorf("dns.servers: %w", err)
		}
		addrKey, err := soleField(obj, "address")
		if err != nil {
			return nil, fmt.Errorf("dns.servers: %w", err)
		}
		var addr string
		if addrKey != "" {
			if err := json.Unmarshal(obj[addrKey], &addr); err != nil {
				return nil, fmt.Errorf("dns.servers: address: %w", err)
			}
		}
		out = append(out, addr)
	}
	return out, nil
}

// asksSystem reports whether a dns server's lookups go through the system
// rather than through the core's routing. An empty address is the core's
// default, which is the system resolver.
func asksSystem(addr string) bool {
	a := strings.ToLower(strings.TrimSpace(addr))
	return a == "" || a == "localhost" || strings.HasPrefix(a, "fakedns") || strings.Contains(a, "+local")
}

// dnsServerHost is the host part of a dns server address: the URL's host for
// https://, tcp://, quic:// and the like, else the address with any port cut.
func dnsServerHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return ""
		}
		return u.Hostname()
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// takenTag names the first of the tags HijackTunDNS adds that the config
// already uses for an outbound or an inbound, or "" when all are free.
func takenTag(outbounds []map[string]json.RawMessage, cfg map[string]json.RawMessage) string {
	used := map[string]bool{}
	collect := func(items []map[string]json.RawMessage) {
		for _, it := range items {
			key, _ := soleField(it, "tag")
			var tag string
			if key != "" && json.Unmarshal(it[key], &tag) == nil {
				used[tag] = true
			}
		}
	}
	collect(outbounds)
	if inKey, _ := soleField(cfg, "inbounds"); inKey != "" {
		var inbounds []map[string]json.RawMessage
		if json.Unmarshal(cfg[inKey], &inbounds) == nil {
			collect(inbounds)
		}
	}
	for _, tag := range []string{TunDNSOutboundTag, tunDNSLoopTag, tunDNSForwardTag} {
		if used[tag] {
			return tag
		}
	}
	return ""
}

// isFreedom reports whether the outbound is a freedom one.
func isFreedom(ob map[string]json.RawMessage) bool {
	key, _ := soleField(ob, "protocol")
	var protocol string
	return key != "" && json.Unmarshal(ob[key], &protocol) == nil && protocol == "freedom"
}

// freedomHasStrategy reports whether the panel already told this freedom
// outbound how to resolve, in its settings: a strategy other than AsIs reads
// the core's DNS by itself. The sockopt one withStrategy checks on its own.
func freedomHasStrategy(ob map[string]json.RawMessage) bool {
	key, _ := soleField(ob, "settings")
	if key == "" {
		return false
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(ob[key], &settings) != nil {
		return false
	}
	dsKey, _ := soleField(settings, "domainStrategy")
	var ds string
	if dsKey == "" || json.Unmarshal(settings[dsKey], &ds) != nil {
		return false
	}
	return ds != "" && !strings.EqualFold(ds, "AsIs")
}

// tunDNSOutbounds builds the dns outbound and the loopback behind it. The
// rules are the core's newer form: hijack takes A and AAAA into the core's
// DNS — all it can answer — and direct lets the rest go on to where it was
// sent, dialled through the loopback rather than straight out, which in a
// tunnel would be either a loop or a leak.
func tunDNSOutbounds(ingress string, hijack bool) ([]map[string]json.RawMessage, error) {
	var rules []map[string]any
	if hijack {
		rules = append(rules, map[string]any{"action": "hijack", "qType": "1,28"})
	}
	rules = append(rules, map[string]any{"action": "direct"})
	settings := map[string]any{"rules": rules}
	if ingress != "" {
		settings["rewriteAddress"] = "1.1.1.1"
	}
	dnsOut := map[string]any{
		"tag":            TunDNSOutboundTag,
		"protocol":       "dns",
		"settings":       settings,
		"streamSettings": map[string]any{"sockopt": map[string]any{"dialerProxy": tunDNSLoopTag}},
	}
	loop := map[string]any{
		"tag":      tunDNSLoopTag,
		"protocol": "loopback",
		"settings": map[string]any{"inboundTag": tunDNSForwardTag},
	}
	var out []map[string]json.RawMessage
	for _, ob := range []map[string]any{dnsOut, loop} {
		b, err := json.Marshal(ob)
		if err != nil {
			return nil, err
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// addTunDNSRule puts the DNS rule ahead of any panel rules that would
// otherwise claim the query by its address.
func addTunDNSRule(cfg map[string]json.RawMessage, ingress string) error {
	routingKey, err := soleField(cfg, "routing")
	if err != nil {
		return err
	}
	if routingKey == "" {
		routingKey = "routing"
	}
	routing, err := object(cfg[routingKey], "routing")
	if err != nil {
		return err
	}
	rulesKey, err := soleField(routing, "rules")
	if err != nil {
		return fmt.Errorf("routing: %w", err)
	}
	if rulesKey == "" {
		rulesKey = "rules"
	}
	var rules []json.RawMessage
	if len(routing[rulesKey]) > 0 && string(routing[rulesKey]) != "null" {
		if err := json.Unmarshal(routing[rulesKey], &rules); err != nil {
			return fmt.Errorf("routing.rules: %w", err)
		}
	}
	match := map[string]any{
		"type":        "field",
		"inboundTag":  []string{TunInboundTag},
		"port":        "53",
		"outboundTag": TunDNSOutboundTag,
	}
	if ingress != "" {
		match["ip"] = []string{ingress}
	}
	rule, err := json.Marshal(match)
	if err != nil {
		return err
	}
	if routing[rulesKey], err = json.Marshal(append([]json.RawMessage{rule}, rules...)); err != nil {
		return err
	}
	if cfg[routingKey], err = json.Marshal(routing); err != nil {
		return err
	}
	return nil
}
