package xraycfg

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// BuildTrojanOutbound mirrors BuildVLESSOutbound for trojan links. The password
// is the URL userinfo; transport and TLS/reality settings are shared with vless
// via setTransportSettings/setSecuritySettings.
func BuildTrojanOutbound(u *url.URL) (*TrojanOutbound, error) {
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("parse host:port: %w", err)
	}
	if strings.Contains(host, ":") {
		return nil, fmt.Errorf("IPv6 addresses are not supported (IPv6 is disabled): %s", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	password := u.User.Username()
	q := u.Query()

	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}

	ss := &StreamSettings{Network: network}
	setTransportSettings(ss, network, q)
	setSecuritySettings(ss, q)

	return &TrojanOutbound{
		Tag:      "proxy",
		Protocol: "trojan",
		Settings: &TrojanSettings{
			Servers: []TrojanServer{
				{Address: host, Port: port, Password: password},
			},
		},
		Stream: ss,
	}, nil
}
