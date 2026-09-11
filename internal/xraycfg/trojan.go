package xraycfg

import (
	"net/url"
)

// BuildTrojanOutbound mirrors BuildVLESSOutbound for trojan links. The password
// is the URL userinfo; transport and TLS/reality settings are shared with vless
// via setTransportSettings/setSecuritySettings.
func BuildTrojanOutbound(u *url.URL) (*TrojanOutbound, error) {
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	password := u.User.Username()
	q := u.Query()

	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}

	ss := &StreamSettings{Network: network}
	setTransportSettings(ss, network, q)
	if err := setSecuritySettings(ss, q); err != nil {
		return nil, err
	}

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
