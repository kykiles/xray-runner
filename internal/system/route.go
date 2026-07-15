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
}
