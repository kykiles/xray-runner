package xraycfg

import (
	"encoding/json"
) 

type XrayConfig struct {
	Log       *LogConfig       `json:"log,omitempty"`
	DNS       json.RawMessage  `json:"dns,omitempty"`
	Inbounds  []Inbound        `json:"inbounds,omitempty"`
	Outbounds []json.RawMessage `json:"outbounds,omitempty"`
	Routing   json.RawMessage  `json:"routing,omitempty"`
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
	Enabled     bool     `json:"enabled"`
	RouteOnly   bool     `json:"routeOnly,omitempty"`
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
	Tag      string        `json:"tag"`
	Protocol string        `json:"protocol"`
	Settings *SSSettings   `json:"settings,omitempty"`
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
	Tag      string           `json:"tag"`
	Protocol string           `json:"protocol"`
	Settings *HysteriaProtoSettings `json:"settings,omitempty"`
	Stream   *StreamSettings  `json:"streamSettings,omitempty"`
}

type HysteriaProtoSettings struct {
	Version int    `json:"version"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type HysteriaTransportSettings struct {
	Version      int    `json:"version"`
	Auth         string `json:"auth,omitempty"`
	Up           string `json:"up,omitempty"`
	Down         string `json:"down,omitempty"`
	Congestion   string `json:"congestion,omitempty"`
}

type StreamSettings struct {
	Network             string                      `json:"network,omitempty"`
	Security            string                      `json:"security,omitempty"`
	WSSettings          *WSSettings                 `json:"wsSettings,omitempty"`
	GRPCSettings        *GRPCSettings               `json:"grpcSettings,omitempty"`
	TLSSettings         *TLSSettings                `json:"tlsSettings,omitempty"`
	Reality             *RealitySettings            `json:"realitySettings,omitempty"`
	HysteriaSettings    *HysteriaTransportSettings  `json:"hysteriaSettings,omitempty"`
}

type WSSettings struct {
	Path    string `json:"path,omitempty"`
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
}

type TUNSettings struct {
	MTU           int      `json:"mtu"`
	Address       []string `json:"address"`
	Networks      []string `json:"networks"`
	InterfaceName string   `json:"interfaceName"`
}

func BuildTUNInbound() Inbound {
	return Inbound{
		Tag:      "tun",
		Protocol: "tun",
		Settings: json.RawMessage(`{"mtu":9000,"address":["10.0.0.1/24"],"networks":["tcp","udp"],"interfaceName":"xray-tun"}`),
		Sniffing: &SniffingConfig{
			Enabled:       true,
			RouteOnly:     true,
			DestOverride:  []string{"http", "tls", "quic"},
		},
	}
}
