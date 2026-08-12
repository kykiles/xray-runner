package xraycfg

import "encoding/json"

const TunInterfaceName = "xray-tun"

// TunAddr is the TUN interface address. The routing layer needs it as the next
// hop when pointing the system's default traffic at the tunnel.
const TunAddr = "10.0.0.1"

// TunAddr6 is the IPv6 counterpart, a ULA prefix: without an address of its own
// the interface takes no IPv6 at all, and every v6-capable app walks past the
// tunnel with its real address. Only the split mode claims it — see ADR-0003.
const TunAddr6 = "fdfe:dcba:9876::1"

type XrayConfig struct {
	Log       *LogConfig        `json:"log,omitempty"`
	DNS       json.RawMessage   `json:"dns,omitempty"`
	Inbounds  []Inbound         `json:"inbounds,omitempty"`
	Outbounds []json.RawMessage `json:"outbounds,omitempty"`
	Routing   json.RawMessage   `json:"routing,omitempty"`
}

type LogConfig struct {
	Loglevel string `json:"loglevel"`
}

type Inbound struct {
	Tag      string          `json:"tag"`
	Port     int             `json:"port"`
	Listen   string          `json:"listen"`
	Protocol string          `json:"protocol"`
	Settings json.RawMessage `json:"settings"`
	Sniffing *SniffingConfig `json:"sniffing,omitempty"`
}

type SniffingConfig struct {
	Enabled      bool     `json:"enabled"`
	RouteOnly    bool     `json:"routeOnly,omitempty"`
	DestOverride []string `json:"destOverride,omitempty"`
}

type VLESSOutbound struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Settings *VLESSSettings  `json:"settings,omitempty"`
	Stream   *StreamSettings `json:"streamSettings,omitempty"`
}

type VLESSSettings struct {
	VNext []VNextServer `json:"vnext,omitempty"`
}

type VNextServer struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []VLessUser `json:"users"`
}

type VLessUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
	Flow       string `json:"flow,omitempty"`
}

type SSOutbound struct {
	Tag      string      `json:"tag"`
	Protocol string      `json:"protocol"`
	Settings *SSSettings `json:"settings,omitempty"`
}

type SSSettings struct {
	Servers []SSServer `json:"servers,omitempty"`
}

type SSServer struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Method   string `json:"method"`
	Password string `json:"password"`
	Level    int    `json:"level"`
}

type VMessOutbound struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Settings *VMessSettings  `json:"settings,omitempty"`
	Stream   *StreamSettings `json:"streamSettings,omitempty"`
}

type VMessSettings struct {
	VNext []VMessServer `json:"vnext,omitempty"`
}

type VMessServer struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []VMessUser `json:"users"`
}

type VMessUser struct {
	ID       string `json:"id"`
	Security string `json:"security"`
	AlterID  int    `json:"alterId"`
}

type HysteriaOutbound struct {
	Tag      string                 `json:"tag"`
	Protocol string                 `json:"protocol"`
	Settings *HysteriaProtoSettings `json:"settings,omitempty"`
	Stream   *StreamSettings        `json:"streamSettings,omitempty"`
}

type HysteriaProtoSettings struct {
	Version int    `json:"version"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type HysteriaTransportSettings struct {
	Version    int    `json:"version"`
	Auth       string `json:"auth,omitempty"`
	Up         string `json:"up,omitempty"`
	Down       string `json:"down,omitempty"`
	Congestion string `json:"congestion,omitempty"`
}

type StreamSettings struct {
	Network             string                     `json:"network,omitempty"`
	Security            string                     `json:"security,omitempty"`
	WSSettings          *WSSettings                `json:"wsSettings,omitempty"`
	GRPCSettings        *GRPCSettings              `json:"grpcSettings,omitempty"`
	XHTTPSettings       *XHTTPSettings             `json:"xhttpSettings,omitempty"`
	HTTPUpgradeSettings *HTTPUpgradeSettings       `json:"httpupgradeSettings,omitempty"`
	TLSSettings         *TLSSettings               `json:"tlsSettings,omitempty"`
	Reality             *RealitySettings           `json:"realitySettings,omitempty"`
	HysteriaSettings    *HysteriaTransportSettings `json:"hysteriaSettings,omitempty"`
}

type WSSettings struct {
	Path    string     `json:"path,omitempty"`
	Headers *WSHeaders `json:"headers,omitempty"`
}

type WSHeaders struct {
	Host string `json:"Host,omitempty"`
}

type GRPCSettings struct {
	ServiceName string `json:"serviceName,omitempty"`
	MultiMode   bool   `json:"multiMode,omitempty"`
	Authority   string `json:"authority,omitempty"`
}

// XHTTPSettings covers the fields a bare xhttp URL can carry. Panel configs with
// richer settings (extra/xmux) go through the RAW path and never reach here.
type XHTTPSettings struct {
	Path string `json:"path,omitempty"`
	Host string `json:"host,omitempty"`
	Mode string `json:"mode,omitempty"`
}

type HTTPUpgradeSettings struct {
	Path string `json:"path,omitempty"`
	Host string `json:"host,omitempty"`
}

type TLSSettings struct {
	ServerName    string   `json:"serverName,omitempty"`
	Fingerprint   string   `json:"fingerprint,omitempty"`
	ALPN          []string `json:"alpn,omitempty"`
	AllowInsecure bool     `json:"allowInsecure,omitempty"`
}

type RealitySettings struct {
	ServerName  string `json:"serverName,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	ShortID     string `json:"shortId,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	SpiderX     string `json:"spiderX,omitempty"`
}

type TrojanOutbound struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Settings *TrojanSettings `json:"settings,omitempty"`
	Stream   *StreamSettings `json:"streamSettings,omitempty"`
}

type TrojanSettings struct {
	Servers []TrojanServer `json:"servers,omitempty"`
}

type TrojanServer struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

// TUNSettings is the tun inbound's settings block. The addresses go in
// "gateway": xray drops keys it does not know, and the "address"/"networks"
// pair this used to send was among them — the interface came up on xray's own
// default, which happens to be TunAddr. That coincidence held for IPv4 and
// would have quietly left IPv6 unassigned.
type TUNSettings struct {
	MTU           int      `json:"mtu"`
	Gateway       []string `json:"gateway"`
	InterfaceName string   `json:"name"`
}

// BuildTUNInbound builds the tun inbound. With ipv6 the interface also takes a
// v6 address, which is what lets the routing layer pull IPv6 into the tunnel.
func BuildTUNInbound(ipv6 bool) Inbound {
	gateway := []string{TunAddr + "/24"}
	if ipv6 {
		gateway = append(gateway, TunAddr6+"/126")
	}
	// The settings are a fixed struct, so marshalling cannot fail.
	settings, _ := json.Marshal(TUNSettings{
		MTU:           9000,
		Gateway:       gateway,
		InterfaceName: TunInterfaceName,
	})
	return Inbound{
		Tag:      "tun",
		Protocol: "tun",
		Settings: settings,
		Sniffing: &SniffingConfig{
			Enabled:      true,
			RouteOnly:    true,
			DestOverride: []string{"http", "tls", "quic"},
		},
	}
}
