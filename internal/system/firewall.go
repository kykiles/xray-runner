package system

// Endpoint is one VPN server the kill switch must let through: an address, the
// port xray dials it on, and whether that happens over UDP (hysteria2) or TCP.
type Endpoint struct {
	IP   string // resolved server IP
	Port int    // server port
	UDP  bool   // hysteria2 uses UDP; other protocols use TCP
}

// KillSwitchConfig describes the exceptions the kill switch must keep open so
// the VPN can (re)connect to its servers, while everything else is blocked.
// Without them the kill switch also blocks xray's own new connections to the
// VPS and the tunnel can never come back after a drop.
//
// Endpoints is a list rather than one server because a session runs under the
// panel's routing: a rule may send some domains through a sibling outbound of
// the same profile, and a sibling left out of the whitelist is a silent partial
// blackhole. Empty → no server exception at all.
type KillSwitchConfig struct {
	Endpoints []Endpoint
	// Core is the path of the xray binary the session runs. Windows lets the
	// program itself through, which is how traffic the panel routes `direct`
	// leaves the physical adapter; Linux lets it through by its socket mark
	// instead and ignores this.
	Core string
	// PassDNS lets DNS (port 53) past the tunnel. A session sets it when the
	// system's DNS still takes the physical path (EnableTunDNS failed or the
	// platform does not switch it): closed, every lookup would fail. With the
	// DNS in the tunnel the port stays shut — the lookups that would use it are
	// the system's own while the tunnel is down between two cores, and the
	// names would go to the provider.
	PassDNS bool
}
