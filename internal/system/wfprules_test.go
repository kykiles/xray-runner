package system

import (
	"net/netip"
	"testing"
)

// flow is one connection as WFP's ALE layers see it, for evalRules.
type flow struct {
	layer    wfpLayer
	loopback bool
	local    string
	remote   string
	proto    uint16
	lport    uint16
	rport    uint16
	core     bool // opened by the core
}

// evalRules decides a flow the way WFP decides it within our sublayer: every
// permit outweighs every block, and a filter matches when all its conditions
// do. No filter at all on the layer means the flow is not ours to decide.
func evalRules(t *testing.T, rules []wfpRule, f flow) (permit, decided bool) {
	t.Helper()
	match := func(r wfpRule) bool {
		if r.layer != f.layer {
			return false
		}
		for _, c := range r.conds {
			ok := false
			switch c.field {
			case fieldLoopback:
				ok = f.loopback
			case fieldLocalAddr:
				ok = f.local != "" && c.addr == netip.MustParseAddr(f.local)
			case fieldRemoteAddr:
				ok = f.remote != "" && c.addr == netip.MustParseAddr(f.remote)
			case fieldProtocol:
				ok = c.num == f.proto
			case fieldLocalPort:
				ok = c.num == f.lport
			case fieldRemotePort:
				ok = c.num == f.rport
			case fieldApp:
				ok = f.core
			default:
				t.Fatalf("unknown field %d", c.field)
			}
			if !ok {
				return false
			}
		}
		return true
	}
	blocked := false
	for _, r := range rules {
		if !match(r) {
			continue
		}
		if r.permit {
			return true, true
		}
		blocked = true
	}
	return false, blocked
}

func TestKillSwitchRules_Decisions(t *testing.T) {
	cfg := KillSwitchConfig{Endpoints: []Endpoint{
		{IP: "203.0.113.7", Port: 443},
		{IP: "2001:db8::7", Port: 8443, UDP: true},
		{IP: "::ffff:198.51.100.9", Port: 80},
	}}
	rules, err := killSwitchRules(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	const phys4, phys6 = "192.168.1.10", "2001:db8:1::10"
	cases := []struct {
		name string
		f    flow
		want bool
	}{
		{"app on the physical path", flow{layer: layerConnect4, local: phys4, remote: "1.1.1.1", proto: protoTCP, rport: 443}, false},
		{"app over IPv6 on the physical path", flow{layer: layerConnect6, local: phys6, remote: "2606:4700::1111", proto: protoTCP, rport: 443}, false},
		{"UDP on the physical path", flow{layer: layerConnect4, local: phys4, remote: "1.1.1.1", proto: protoUDP, rport: 443}, false},
		{"inbound on the physical path", flow{layer: layerAccept4, local: phys4, remote: "192.168.1.20", proto: protoTCP, lport: 445}, false},
		{"app through the tunnel", flow{layer: layerConnect4, local: "10.0.0.1", remote: "1.1.1.1", proto: protoTCP, rport: 443}, true},
		{"app through the tunnel over IPv6", flow{layer: layerConnect6, local: "fdfe:dcba:9876::1", remote: "2606:4700::1111", proto: protoTCP, rport: 443}, true},
		{"loopback", flow{layer: layerConnect4, loopback: true, local: "127.0.0.1", remote: "127.0.0.1", proto: protoTCP, rport: 10808}, true},
		{"loopback inbound", flow{layer: layerAccept6, loopback: true, local: "::1", remote: "::1", proto: protoTCP, lport: 10808}, true},
		{"core direct", flow{layer: layerConnect4, local: phys4, remote: "93.184.216.34", proto: protoTCP, rport: 443, core: true}, true},
		{"core does not listen on the physical path", flow{layer: layerAccept4, local: phys4, remote: "192.168.1.20", proto: protoTCP, lport: 10808, core: true}, false},
		{"server", flow{layer: layerConnect4, local: phys4, remote: "203.0.113.7", proto: protoTCP, rport: 443}, true},
		{"server, other port", flow{layer: layerConnect4, local: phys4, remote: "203.0.113.7", proto: protoTCP, rport: 22}, false},
		{"server, other protocol", flow{layer: layerConnect4, local: phys4, remote: "203.0.113.7", proto: protoUDP, rport: 443}, false},
		{"IPv6 server over UDP", flow{layer: layerConnect6, local: phys6, remote: "2001:db8::7", proto: protoUDP, rport: 8443}, true},
		{"mapped server lands on IPv4", flow{layer: layerConnect4, local: phys4, remote: "198.51.100.9", proto: protoTCP, rport: 80}, true},
		{"DNS", flow{layer: layerConnect4, local: phys4, remote: "8.8.8.8", proto: protoUDP, rport: 53}, true},
		{"DNS over TCP, IPv6", flow{layer: layerConnect6, local: phys6, remote: "2001:4860:4860::8888", proto: protoTCP, rport: 53}, true},
		{"DHCP", flow{layer: layerConnect4, local: "0.0.0.0", remote: "255.255.255.255", proto: protoUDP, lport: 68, rport: 67}, true},
		{"DHCP reply", flow{layer: layerAccept4, local: phys4, remote: "192.168.1.1", proto: protoUDP, lport: 68, rport: 67}, true},
		{"DHCPv6", flow{layer: layerConnect6, local: "fe80::1", remote: "ff02::1:2", proto: protoUDP, lport: 546, rport: 547}, true},
		{"neighbour solicitation", flow{layer: layerConnect6, local: "fe80::1", remote: "ff02::1:ff00:1", proto: protoICMPv6, lport: 135}, true},
		{"ICMPv6 echo", flow{layer: layerConnect6, local: phys6, remote: "2606:4700::1111", proto: protoICMPv6, lport: 128}, false},
	}
	for _, c := range cases {
		got, decided := evalRules(t, rules, c.f)
		if !decided {
			t.Errorf("%s: no filter decides the flow", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s: permitted = %v, want %v", c.name, got, c.want)
		}
	}
}

// Without the core's App ID the direct path is closed — a core the kill switch
// could not name is not let through by something broader.
func TestKillSwitchRules_NoCore(t *testing.T) {
	rules, err := killSwitchRules(KillSwitchConfig{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		for _, c := range r.conds {
			if c.field == fieldApp {
				t.Fatalf("rule %q names the core, but there is no App ID", r.name)
			}
		}
	}
	if got, _ := evalRules(t, rules, flow{layer: layerConnect4, local: "192.168.1.10", remote: "1.1.1.1", proto: protoTCP, rport: 443, core: true}); got {
		t.Fatal("the core's direct traffic passed without a rule for it")
	}
}

// Every layer is closed, and no permit is unconditional: one without
// conditions would open the layer it sits on.
func TestKillSwitchRules_Shape(t *testing.T) {
	rules, err := killSwitchRules(KillSwitchConfig{Endpoints: []Endpoint{{IP: "203.0.113.7", Port: 443}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	blocked := map[wfpLayer]bool{}
	for _, r := range rules {
		if !r.permit {
			if len(r.conds) != 0 {
				t.Errorf("block on layer %d has conditions %v", r.layer, r.conds)
			}
			blocked[r.layer] = true
			continue
		}
		if len(r.conds) == 0 {
			t.Errorf("permit %q on layer %d has no conditions", r.name, r.layer)
		}
		for _, c := range r.conds {
			if (c.field == fieldLocalAddr || c.field == fieldRemoteAddr) && c.addr.Is6() != r.layer.v6() {
				t.Errorf("permit %q: address %s on layer %d of the other family", r.name, c.addr, r.layer)
			}
		}
	}
	for _, l := range []wfpLayer{layerConnect4, layerConnect6, layerAccept4, layerAccept6} {
		if !blocked[l] {
			t.Errorf("layer %d is not blocked", l)
		}
	}
}

func TestKillSwitchRules_BadEndpoint(t *testing.T) {
	for _, e := range []Endpoint{{IP: "", Port: 443}, {IP: "example.com", Port: 443}, {IP: "1.2.3.4", Port: 0}, {IP: "1.2.3.4", Port: 70000}, {IP: "fe80::1%eth0", Port: 443}} {
		if _, err := killSwitchRules(KillSwitchConfig{Endpoints: []Endpoint{e}}, false); err == nil {
			t.Errorf("endpoint %+v accepted", e)
		}
	}
}
