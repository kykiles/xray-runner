package system

// TunRouteConfig describes the routes that make the OS actually use the TUN
// device. Xray only creates the interface and reads packets off it — unlike
// sing-box's auto_route it never touches the routing table, so without these
// routes the tun exists, reports UP, and receives no traffic at all.
type TunRouteConfig struct {
	Iface string // TUN interface name (xray-tun)
	Addr  string // TUN interface address, used as the next hop
	// ServerIPs are the VPN server IPs kept on the physical path. A balancer
	// profile rotates across several servers, and every one of them needs an
	// exception — a server left inside the tunnel deadlocks xray's uplink.
	ServerIPs []string
	// Addr6 and ServerIPs6 are the IPv6 half, and empty Addr6 means the tunnel
	// claims IPv4 only — which is what tun mode still does. Split mode fills
	// them in: there a v6-capable app would otherwise prefer the AAAA record and
	// walk past the tunnel with its real address (ADR-0003).
	Addr6      string
	ServerIPs6 []string
}
