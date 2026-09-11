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
	// Addr6 and ServerIPs6 are the IPv6 half; empty Addr6 leaves IPv6 alone.
	// Every tun session fills them in, since a v6-capable app would otherwise
	// prefer the AAAA record and walk past the tunnel with its real address
	// (A11). Windows routes IPv6 into the tunnel; Linux refuses it outright —
	// the VPN runs without IPv6 — and has no use for ServerIPs6.
	Addr6      string
	ServerIPs6 []string
}
