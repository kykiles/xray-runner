package system

// KillSwitchConfig describes the exception the kill switch must keep open so
// the VPN can (re)connect to its server, while everything else is blocked.
// Without it the kill switch also blocks xray's own new connections to the VPS
// and the tunnel can never come back after a drop.
type KillSwitchConfig struct {
	ServerIP   string // resolved VPN server IP (empty → no server exception)
	ServerPort int    // VPN server port
	UDP        bool   // hysteria2 uses UDP; other protocols use TCP
	XrayPath   string // path to the xray binary (Windows allow-rule)
}
