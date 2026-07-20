package xraycfg

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

func BuildSSOutbound(u *url.URL) (*SSOutbound, error) {
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}

	b64 := u.User.Username()
	if m := len(b64) % 4; m != 0 {
		b64 += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64 userinfo: %w", err)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid ss userinfo format, expected method:password")
	}
	method := parts[0]
	password := parts[1]

	return &SSOutbound{
		Tag:      "proxy",
		Protocol: "shadowsocks",
		Settings: &SSSettings{
			Servers: []SSServer{
				{Address: host, Port: port, Method: method, Password: password, Level: 0},
			},
		},
	}, nil
}
