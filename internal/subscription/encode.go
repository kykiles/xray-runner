package subscription

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// EncodeURL is the inverse of parseURL: it reconstructs a bare share link from
// a parsed SubEntry. Credentials are emitted in the clear — masking is a
// display concern the dump utility intentionally bypasses.
func EncodeURL(e *SubEntry) (string, error) {
	switch e.Protocol {
	case "vless":
		return encodeVLESS(e), nil
	case "ss":
		return encodeSS(e), nil
	case "vmess":
		return encodeVMess(e)
	case "hysteria2", "hysteria":
		return encodeHysteria2(e), nil
	default:
		return "", fmt.Errorf("unsupported protocol: %s", e.Protocol)
	}
}

func encodeVLESS(e *SubEntry) string {
	q := url.Values{}
	q.Set("type", orDefault(e.Network, "tcp"))
	setIf(q, "flow", e.Flow)
	setIf(q, "security", e.Security)
	setIf(q, "path", e.Path)
	setIf(q, "host", e.Host)
	setIf(q, "sni", e.SNI)
	setIf(q, "fp", e.Fingerprint)
	setIf(q, "pbk", e.PublicKey)
	setIf(q, "sid", e.ShortID)
	setIf(q, "alpn", e.ALPN)
	setIf(q, "serviceName", e.ServiceName)

	u := &url.URL{
		Scheme:   "vless",
		User:     url.User(e.UUID),
		Host:     net.JoinHostPort(e.Address, strconv.Itoa(e.Port)),
		RawQuery: q.Encode(),
		Fragment: e.Remarks,
	}
	return u.String()
}

func encodeSS(e *SubEntry) string {
	u := &url.URL{
		Scheme:   "ss",
		User:     url.User(base64StdEncode(e.Method + ":" + e.Password)),
		Host:     net.JoinHostPort(e.Address, strconv.Itoa(e.Port)),
		Fragment: e.Remarks,
	}
	return u.String()
}

func encodeVMess(e *SubEntry) (string, error) {
	obj := map[string]interface{}{
		"v":    "2",
		"add":  e.Address,
		"port": strconv.Itoa(e.Port),
		"id":   e.UUID,
		"net":  orDefault(e.Network, "tcp"),
		"ps":   e.Remarks,
	}
	setStr(obj, "path", e.Path)
	setStr(obj, "host", e.Host)
	setStr(obj, "alpn", e.ALPN)
	setStr(obj, "type", e.Encryption)
	if e.Security == "tls" {
		obj["tls"] = "tls"
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("encode vmess: %w", err)
	}
	return "vmess://" + base64StdEncode(string(raw)), nil
}

func encodeHysteria2(e *SubEntry) string {
	q := url.Values{}
	setIf(q, "sni", e.SNI)
	setIf(q, "alpn", e.ALPN)
	setIf(q, "up", e.Up)
	setIf(q, "down", e.Down)
	setIf(q, "obfs", e.Obfs)
	setIf(q, "obfs-password", e.ObfsPassword)
	setIf(q, "congestion", e.Congestion)
	if e.Insecure {
		q.Set("insecure", "1")
	}

	u := &url.URL{
		Scheme:   "hysteria2",
		User:     url.User(e.Password),
		Host:     net.JoinHostPort(e.Address, strconv.Itoa(e.Port)),
		RawQuery: q.Encode(),
		Fragment: e.Remarks,
	}
	return u.String()
}

func setIf(q url.Values, key, val string) {
	if val != "" {
		q.Set(key, val)
	}
}

func setStr(obj map[string]interface{}, key, val string) {
	if val != "" {
		obj[key] = val
	}
}
