package xraycfg

import (
	"encoding/json"
	"net"
	"strconv"
)

// Server is one remote endpoint an outbound dials.
type Server struct {
	Protocol string
	Host     string
	Port     int
}

// OutboundServers lists every remote endpoint the outbounds dial, in order and
// without repeats: the vnext and servers lists, the inline address of
// hysteria and the flat vless form, and wireguard's peer endpoints. The
// service keeps them off the tunnel and in the kill switch's whitelist; it
// reads them off the outbounds it runs rather than taking a list from the
// client, so the two cannot disagree.
func OutboundServers(raw json.RawMessage) []Server {
	var outbounds []struct {
		Protocol string `json:"protocol"`
		Settings struct {
			Vnext   []hostPort `json:"vnext"`
			Servers []hostPort `json:"servers"`
			Address string     `json:"address"`
			Port    int        `json:"port"`
			Peers   []struct {
				Endpoint string `json:"endpoint"`
			} `json:"peers"`
		} `json:"settings"`
	}
	if json.Unmarshal(raw, &outbounds) != nil {
		return nil
	}
	var out []Server
	seen := map[Server]bool{}
	add := func(s Server) {
		if s.Host == "" || s.Port <= 0 || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, ob := range outbounds {
		switch ob.Protocol {
		case "freedom", "blackhole", "dns", "loopback", "":
			continue
		}
		for _, hp := range append(ob.Settings.Vnext, ob.Settings.Servers...) {
			add(Server{Protocol: ob.Protocol, Host: hp.Address, Port: hp.Port})
		}
		add(Server{Protocol: ob.Protocol, Host: ob.Settings.Address, Port: ob.Settings.Port})
		for _, p := range ob.Settings.Peers {
			host, port, err := net.SplitHostPort(p.Endpoint)
			if err != nil {
				continue
			}
			n, _ := strconv.Atoi(port)
			add(Server{Protocol: ob.Protocol, Host: host, Port: n})
		}
	}
	return out
}

type hostPort struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// UDPProtocol reports whether the protocol reaches its server over UDP.
func UDPProtocol(p string) bool {
	return p == "hysteria2" || p == "hysteria" || p == "wireguard"
}
