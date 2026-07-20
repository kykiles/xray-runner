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
	XrayPath  string // path to the xray binary (Windows allow-rule)
}
