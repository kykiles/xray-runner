package xraycfg

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

func BuildSSOutbound(u *url.URL) *SSOutbound {
	host, portStr, _ := strings.Cut(u.Host, ":")
	if host == "" {
		host = u.Host
	}
	port, _ := strconv.Atoi(portStr)
	if port == 0 {
		port = 443
	}

	b64 := u.User.Username()
	if m := len(b64) % 4; m != 0 {
		b64 += strings.Repeat("=", 4-m)
	}
	decoded, _ := base64.StdEncoding.DecodeString(b64)
	parts := strings.SplitN(string(decoded), ":", 2)
	method := "aes-256-gcm"
	password := ""
	if len(parts) == 2 {
		method = parts[0]
		password = parts[1]
	}

	return &SSOutbound{
		Tag:      "proxy",
		Protocol: "shadowsocks",
		Settings: &SSSettings{
			Servers: []SSServer{
				{Address: host, Port: port, Method: method, Password: password, Level: 0},
			},
		},
	}
}
