package xraycfg

import (
	"net/url"
	"strconv"
	"strings"
)

func BuildVLESSOutbound(u *url.URL) *VLESSOutbound {
	host, portStr, _ := strings.Cut(u.Host, ":")
	if host == "" {
		host = u.Host
	}
	port, _ := strconv.Atoi(portStr)
	if port == 0 {
		port = 443
	}
	uuid := u.User.Username()
	q := u.Query()

	us := VLessUser{
		ID:         uuid,
		Encryption: "none",
	}
	if flow := q.Get("flow"); flow != "" {
		us.Flow = flow
	}

	network := q.Get("type")
	if network == "" {
		network = "tcp"
	}

	ss := &StreamSettings{Network: network}
	setTransportSettings(ss, network, q)
	setSecuritySettings(ss, q)

	return &VLESSOutbound{
		Tag:      "proxy",
		Protocol: "vless",
		Settings: &VLESSSettings{
			VNext: []VNextServer{
				{Address: host, Port: port, Users: []VLessUser{us}},
			},
		},
		Stream: ss,
	}
}

func setTransportSettings(ss *StreamSettings, network string, q url.Values) {
	switch network {
	case "ws":
		ws := &WSSettings{}
		if p := q.Get("path"); p != "" {
			ws.Path = p
		}
		if h := q.Get("host"); h != "" {
			ws.Headers = &WSHeaders{Host: h}
		}
		ss.WSSettings = ws

	case "grpc":
		grpc := &GRPCSettings{}
		if svc := q.Get("serviceName"); svc != "" {
			grpc.ServiceName = svc
		}
		if q.Get("mode") == "multi" {
			grpc.MultiMode = true
		}
		if auth := q.Get("authority"); auth != "" {
			grpc.Authority = auth
		}
		ss.GRPCSettings = grpc
	}
}

func setSecuritySettings(ss *StreamSettings, q url.Values) {
	security := q.Get("security")
	switch security {
	case "reality":
		ss.Security = "reality"
		ss.Reality = &RealitySettings{
			ServerName:  q.Get("sni"),
			PublicKey:   q.Get("pbk"),
			ShortID:     q.Get("sid"),
			Fingerprint: q.Get("fp"),
		}
	case "tls":
		ss.Security = "tls"
		tls := &TLSSettings{}
		sni := q.Get("sni")
		if sni == "" {
			sni = q.Get("host")
		}
		if sni != "" {
			tls.ServerName = sni
		}
		if fp := q.Get("fp"); fp != "" {
			tls.Fingerprint = fp
		}
		if alpn := q.Get("alpn"); alpn != "" {
			tls.ALPN = strings.Split(alpn, ",")
		}
		ss.TLSSettings = tls
	}
}
