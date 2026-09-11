package xraycfg

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// splitHostPort is the address check every builder shares (M-3): IPv6 is
// unsupported while the tunnel runs v4-only, and a port outside 1..65535 must
// fail here rather than deep inside xray at launch.
func splitHostPort(hostport string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", 0, fmt.Errorf("parse host:port: %w", err)
	}
	if strings.Contains(host, ":") {
		return "", 0, fmt.Errorf("IPv6 addresses are not supported (IPv6 is disabled): %s", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range 1..65535", port)
	}
	return host, port, nil
}

func BuildVLESSOutbound(u *url.URL) (*VLESSOutbound, error) {
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
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
	if err := setSecuritySettings(ss, q); err != nil {
		return nil, err
	}

	return &VLESSOutbound{
		Tag:      "proxy",
		Protocol: "vless",
		Settings: &VLESSSettings{
			VNext: []VNextServer{
				{Address: host, Port: port, Users: []VLessUser{us}},
			},
		},
		Stream: ss,
	}, nil
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

	case "xhttp", "splithttp":
		ss.Network = "xhttp"
		xhttp := &XHTTPSettings{}
		if p := q.Get("path"); p != "" {
			xhttp.Path = p
		}
		if h := q.Get("host"); h != "" {
			xhttp.Host = h
		}
		if m := q.Get("mode"); m != "" {
			xhttp.Mode = m
		}
		ss.XHTTPSettings = xhttp

	case "httpupgrade":
		hu := &HTTPUpgradeSettings{}
		if p := q.Get("path"); p != "" {
			hu.Path = p
		}
		if h := q.Get("host"); h != "" {
			hu.Host = h
		}
		ss.HTTPUpgradeSettings = hu
	}
}

// NormalizeSecurity is the one rule for a link's security value (A07): case and
// spaces do not matter, empty and "none" both mean no security layer — there
// are working subscriptions without one — and tls/reality are the only layers
// the builders know. Anything else is refused: a value that matched no case
// used to build an outbound with no protection at all, reality keys dropped
// and the credentials sent in the clear, without a word.
func NormalizeSecurity(v string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(v)); s {
	case "", "none":
		return "", nil
	case "tls", "reality":
		return s, nil
	default:
		return "", fmt.Errorf("не поддерживается: security=%s", v)
	}
}

func setSecuritySettings(ss *StreamSettings, q url.Values) error {
	security, err := NormalizeSecurity(q.Get("security"))
	if err != nil {
		return err
	}
	switch security {
	case "reality":
		ss.Security = "reality"
		ss.Reality = &RealitySettings{
			ServerName:  q.Get("sni"),
			PublicKey:   q.Get("pbk"),
			ShortID:     q.Get("sid"),
			Fingerprint: q.Get("fp"),
			SpiderX:     q.Get("spx"),
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
		// M-1: the caller only sets this after checking its own opt-in, so a
		// subscription alone can never turn certificate verification off.
		if ai := q.Get("allowInsecure"); ai == "1" || ai == "true" {
			tls.AllowInsecure = true
		}
		ss.TLSSettings = tls
	}
	return nil
}
