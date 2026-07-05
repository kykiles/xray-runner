package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"xray-runner/internal/xraycfg"
)

func BuildOutboundJSON(entry *SubEntry) (json.RawMessage, error) {
	switch entry.Protocol {
	case "vless":
		return buildVLESS(entry)
	case "vmess":
		return buildVMess(entry)
	case "ss":
		return buildSS(entry)
	case "hysteria2", "hysteria":
		return buildHysteria2(entry)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", entry.Protocol)
	}
}

func buildVLESS(e *SubEntry) (json.RawMessage, error) {
	q := url.Values{}
	q.Set("type", orDefault(e.Network, "tcp"))
	if e.Flow != "" {
		q.Set("flow", e.Flow)
	}
	if e.Security != "" {
		q.Set("security", e.Security)
	}
	if e.Path != "" {
		q.Set("path", e.Path)
	}
	if e.Host != "" {
		q.Set("host", e.Host)
	}
	if e.SNI != "" {
		q.Set("sni", e.SNI)
	}
	if e.Fingerprint != "" {
		q.Set("fp", e.Fingerprint)
	}
	if e.PublicKey != "" {
		q.Set("pbk", e.PublicKey)
	}
	if e.ShortID != "" {
		q.Set("sid", e.ShortID)
	}
	if e.ALPN != "" {
		q.Set("alpn", e.ALPN)
	}
	if e.ServiceName != "" {
		q.Set("serviceName", e.ServiceName)
	}

	u := &url.URL{
		Scheme:   "vless",
		Host:     net.JoinHostPort(e.Address, strconv.Itoa(e.Port)),
		User:     url.User(e.UUID),
		RawQuery: q.Encode(),
	}

	outbound := xraycfg.BuildVLESSOutbound(u)
	return json.Marshal(outbound)
}

func buildVMess(e *SubEntry) (json.RawMessage, error) {
	ss := &xraycfg.StreamSettings{
		Network:  orDefault(e.Network, "tcp"),
		Security: e.Security,
	}

	switch e.Network {
	case "ws":
		ws := &xraycfg.WSSettings{}
		if e.Path != "" {
			ws.Path = e.Path
		}
		if e.Host != "" {
			ws.Headers = &xraycfg.WSHeaders{Host: e.Host}
		}
		ss.WSSettings = ws
	case "grpc":
		grpc := &xraycfg.GRPCSettings{}
		if e.ServiceName != "" {
			grpc.ServiceName = e.ServiceName
		}
		ss.GRPCSettings = grpc
	}

	switch e.Security {
	case "tls":
		tls := &xraycfg.TLSSettings{}
		sni := e.SNI
		if sni == "" {
			sni = e.Host
		}
		if sni != "" {
			tls.ServerName = sni
		}
		if e.Fingerprint != "" {
			tls.Fingerprint = e.Fingerprint
		}
		if e.ALPN != "" {
			tls.ALPN = strings.Split(e.ALPN, ",")
		}
		ss.TLSSettings = tls
	case "reality":
		ss.Reality = &xraycfg.RealitySettings{
			ServerName:  e.SNI,
			PublicKey:   e.PublicKey,
			ShortID:     e.ShortID,
			Fingerprint: e.Fingerprint,
		}
	}

	security := "auto"
	if e.Encryption != "" {
		security = e.Encryption
	}

	outbound := &xraycfg.VMessOutbound{
		Tag:      "proxy",
		Protocol: "vmess",
		Settings: &xraycfg.VMessSettings{
			VNext: []xraycfg.VMessServer{
				{
					Address: e.Address,
					Port:    e.Port,
					Users: []xraycfg.VMessUser{
						{ID: e.UUID, Security: security, AlterID: 0},
					},
				},
			},
		},
		Stream: ss,
	}

	return json.Marshal(outbound)
}

func buildSS(e *SubEntry) (json.RawMessage, error) {
	b64 := base64StdEncode(e.Method + ":" + e.Password)

	u := &url.URL{
		Scheme: "ss",
		Host:   net.JoinHostPort(e.Address, strconv.Itoa(e.Port)),
		User:   url.User(b64),
	}

	outbound := xraycfg.BuildSSOutbound(u)
	return json.Marshal(outbound)
}

func buildHysteria2(e *SubEntry) (json.RawMessage, error) {
	up := e.Up
	down := e.Down

	if up == "" {
		up = "100 mbps"
	}
	if down == "" {
		down = "100 mbps"
	}

	congestion := e.Congestion
	if congestion == "" {
		congestion = "brutal"
	}

	stream := &xraycfg.StreamSettings{
		Network: "hysteria",
		HysteriaSettings: &xraycfg.HysteriaTransportSettings{
			Version:    2,
			Auth:       e.Password,
			Up:         up,
			Down:       down,
			Congestion: congestion,
		},
	}

	tls := &xraycfg.TLSSettings{
		ALPN: []string{"h3"},
	}
	sni := e.SNI
	if sni == "" {
		sni = e.Address
	}
	tls.ServerName = sni
	if e.Insecure {
		tls.AllowInsecure = true
	}
	stream.TLSSettings = tls
	stream.Security = "tls"

	// Obfs via Obfs object or just pass as-is
	// (Xray Hysteria2 obfuscation handling is via Salamander; we pass obfs fields
	// through hysteriaSettings but Xray doesn't natively support Salamander
	// in streamSettings yet — for now we set it on the transport settings)
	if e.Obfs != "" {
		stream.HysteriaSettings.Auth = stream.HysteriaSettings.Auth + "|" + e.Obfs + ":" + e.ObfsPassword
	}

	outbound := &xraycfg.HysteriaOutbound{
		Tag:      "proxy",
		Protocol: "hysteria",
		Settings: &xraycfg.HysteriaProtoSettings{
			Version: 2,
			Address: e.Address,
			Port:    e.Port,
		},
		Stream: stream,
	}

	return json.Marshal(outbound)
}

func base64StdEncode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
