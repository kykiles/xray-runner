package system

import (
	"fmt"
	"net/netip"

	"xray-runner/internal/xraycfg"
)

// The Windows kill switch as a list of Windows Filtering Platform filters,
// kept apart from the fwpuclnt calls that install them (wfp_windows.go) so the
// set can be read and tested on any system.
//
// Everything sits in one sublayer of this process's own dynamic session:
// blocks at weight 0, permits above them. WFP drops a dynamic session's objects
// when its handle goes, and the handle goes with the process, so a run killed
// outright leaves no filter behind — the kill switch needs no journal entry
// (H08).

// wfpLayer is where a filter applies: at connect (outbound, and the first
// packet of an outbound UDP flow) or at accept (inbound), for one family.
type wfpLayer int

const (
	layerConnect4 wfpLayer = iota
	layerConnect6
	layerAccept4
	layerAccept6
)

func (l wfpLayer) v6() bool { return l == layerConnect6 || l == layerAccept6 }

// wfpField is the condition a filter matches on.
type wfpField int

const (
	fieldLoopback   wfpField = iota // the flow is on loopback (FWPM_CONDITION_FLAGS)
	fieldLocalAddr                  // IP_LOCAL_ADDRESS
	fieldRemoteAddr                 // IP_REMOTE_ADDRESS
	fieldProtocol                   // IP_PROTOCOL
	fieldLocalPort                  // IP_LOCAL_PORT; the ICMP type for ICMP
	fieldRemotePort                 // IP_REMOTE_PORT
	fieldApp                        // ALE_APP_ID: the program behind the socket
)

type wfpCond struct {
	field wfpField
	addr  netip.Addr // fieldLocalAddr, fieldRemoteAddr
	num   uint16     // fieldProtocol, fieldLocalPort, fieldRemotePort
}

type wfpRule struct {
	name   string
	layer  wfpLayer
	permit bool
	conds  []wfpCond
}

const (
	protoICMPv6 = 58
	protoTCP    = 6
	protoUDP    = 17
)

var (
	tunAddr4 = netip.MustParseAddr(xraycfg.TunAddr)
	tunAddr6 = netip.MustParseAddr(xraycfg.TunAddr6)
)

// killSwitchRules is the filter set: everything blocked in both directions and
// both families, but for
//
//   - loopback: the probe inbound, the local proxy and every local service;
//   - the tun's own address. Traffic the routes send into the tunnel leaves
//     with it, so this is "whatever goes through the tunnel". Matched by
//     address rather than by the adapter's LUID: xray makes a new adapter with
//     every core, while the kill switch stays up across restarts;
//   - the core, when core is set: the freedom outbounds are pinned to the
//     physical adapter (DirectBind), so traffic the panel routes `direct`
//     leaves from there — the counterpart of the fwmark rule on Linux;
//   - the VPN servers, by address, port and protocol: the core's uplink, and
//     every reconnect, should the core's own permit not match;
//   - DNS, to port 53 anywhere: Windows resolves names in the DNS Client
//     service rather than in the program that asked, so a reconnect that must
//     look the server up again would otherwise fail. The same exception the
//     Linux kill switch makes;
//   - DHCP (v4 and v6) and neighbour discovery (ICMPv6 133–137): without them
//     the physical adapter loses its lease or its neighbours while the switch
//     is up, and with them the tunnel's own path.
func killSwitchRules(cfg KillSwitchConfig, core bool) ([]wfpRule, error) {
	var rules []wfpRule
	both := func(name string, permit bool, layers []wfpLayer, conds ...wfpCond) {
		for _, l := range layers {
			rules = append(rules, wfpRule{name: name, layer: l, permit: permit, conds: conds})
		}
	}
	all4 := []wfpLayer{layerConnect4, layerAccept4}
	all6 := []wfpLayer{layerConnect6, layerAccept6}
	proto := func(p uint16) wfpCond { return wfpCond{field: fieldProtocol, num: p} }
	lport := func(p uint16) wfpCond { return wfpCond{field: fieldLocalPort, num: p} }
	rport := func(p uint16) wfpCond { return wfpCond{field: fieldRemotePort, num: p} }

	both("loopback", true, append(all4, all6...), wfpCond{field: fieldLoopback})
	both("tun", true, all4, wfpCond{field: fieldLocalAddr, addr: tunAddr4})
	both("tun", true, all6, wfpCond{field: fieldLocalAddr, addr: tunAddr6})
	if core {
		both("core", true, []wfpLayer{layerConnect4, layerConnect6}, wfpCond{field: fieldApp})
	}
	for _, e := range cfg.Endpoints {
		ip, err := netip.ParseAddr(e.IP)
		if err != nil || ip.Zone() != "" || e.Port <= 0 || e.Port > 65535 {
			return nil, fmt.Errorf("kill switch: неверный адрес сервера %s:%d", e.IP, e.Port)
		}
		ip = ip.Unmap()
		l := layerConnect4
		if ip.Is6() {
			l = layerConnect6
		}
		p := uint16(protoTCP)
		if e.UDP {
			p = protoUDP
		}
		both("server", true, []wfpLayer{l}, wfpCond{field: fieldRemoteAddr, addr: ip}, proto(p), rport(uint16(e.Port)))
	}
	for _, p := range []uint16{protoUDP, protoTCP} {
		both("dns", true, []wfpLayer{layerConnect4, layerConnect6}, proto(p), rport(53))
	}
	both("dhcp", true, all4, proto(protoUDP), lport(68), rport(67))
	both("dhcp", true, all6, proto(protoUDP), lport(546), rport(547))
	for typ := uint16(133); typ <= 137; typ++ {
		both("ndp", true, all6, proto(protoICMPv6), lport(typ))
	}
	both("block", false, append(all4, all6...))
	return rules, nil
}
