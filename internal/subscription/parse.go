package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func tryParseJSON(raw []byte) ([]SubEntry, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		if isXrayConfigArray(arr) {
			return parseXrayConfigArray(arr)
		}
		return parseJSONArray(arr)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if servers, ok := obj["servers"]; ok {
			var arr2 []json.RawMessage
			if err := json.Unmarshal(servers, &arr2); err == nil {
				return parseJSONArray(arr2)
			}
		}
	}

	return nil, fmt.Errorf("not valid JSON subscription")
}

func parseJSONArray(arr []json.RawMessage) ([]SubEntry, error) {
	var entries []SubEntry
	for _, item := range arr {
		e, err := parseJSONEntry(item)
		if err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no valid entries found in JSON")
	}
	return entries, nil
}

func parseJSONEntry(raw json.RawMessage) (SubEntry, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return SubEntry{}, err
	}

	// Check protocol first
	if proto := getString(obj, "protocol"); proto == "hysteria2" || proto == "hysteria" {
		return parseXrayJSON(obj), nil
	}

	if _, hasAdd := obj["add"]; hasAdd {
		return parseVMessJSON(obj), nil
	}
	if _, hasProtocol := obj["protocol"]; hasProtocol {
		return parseXrayJSON(obj), nil
	}
	if _, hasServer := obj["server"]; hasServer {
		if _, hasPort := obj["server_port"]; hasPort {
			return parseSSJSON(obj), nil
		}
	}

	return SubEntry{}, fmt.Errorf("unknown JSON format")
}

func parseXrayJSON(obj map[string]interface{}) SubEntry {
	e := SubEntry{
		Protocol:    getString(obj, "protocol"),
		Address:     getString(obj, "address"),
		Port:        getInt(obj, "port", 443),
		UUID:        getString(obj, "uuid"),
		Flow:        getString(obj, "flow"),
		Network:     getString(obj, "network"),
		Security:    getString(obj, "security"),
		Path:        getString(obj, "path"),
		Host:        getString(obj, "host"),
		SNI:         getString(obj, "sni"),
		Fingerprint: getString(obj, "fp"),
		PublicKey:   getString(obj, "publicKey"),
		ShortID:     firstNonEmpty(getString(obj, "shortId"), getString(obj, "sid"), getString(obj, "shortID"), getString(obj, "short_id")),
		ALPN:        getString(obj, "alpn"),
		ServiceName: getString(obj, "serviceName"),
		Method:      getString(obj, "method"),
		Password:    getString(obj, "password"),
		Up:          getString(obj, "up"),
		Down:        getString(obj, "down"),
		Obfs:        getString(obj, "obfs"),
		ObfsPassword: getString(obj, "obfs-password"),
		Congestion:  getString(obj, "congestion"),
	}
	e.Remarks = getString(obj, "remarks")
	if e.Remarks == "" {
		e.Remarks = getString(obj, "ps")
	}

	insecure := getString(obj, "insecure")
	if insecure == "1" || insecure == "true" {
		e.Insecure = true
	}

	// Normalize up/down for hysteria2
	if e.Up != "" && !strings.Contains(strings.ToLower(e.Up), "bps") {
		e.Up = e.Up + " mbps"
	}
	if e.Down != "" && !strings.Contains(strings.ToLower(e.Down), "bps") {
		e.Down = e.Down + " mbps"
	}

	if e.Protocol == "" {
		e.Protocol = "vless"
	}
	if e.Protocol == "hy2" {
		e.Protocol = "hysteria2"
	}
	if e.Network == "" && e.Protocol != "hysteria2" && e.Protocol != "hysteria" {
		e.Network = "tcp"
	}

	if e.Security == "reality" && e.ShortID == "" {
		slog.Debug("REALITY entry has empty shortId (server may accept any)",
			"address", e.Address)
	}
	return e
}

func parseVMessJSON(obj map[string]interface{}) SubEntry {
	e := SubEntry{
		Protocol: "vmess",
		Address:  getString(obj, "add"),
		Port:     getInt(obj, "port", 443),
		UUID:     getString(obj, "id"),
		Network:  getString(obj, "net"),
		Path:     getString(obj, "path"),
		Host:     getString(obj, "host"),
		ALPN:     getString(obj, "alpn"),
	}
	e.Remarks = getString(obj, "ps")
	if e.Remarks == "" {
		e.Remarks = getString(obj, "remarks")
	}

	tls := getString(obj, "tls")
	if tls == "tls" || tls == "1" {
		e.Security = "tls"
	} else if tls != "" {
		e.Security = tls
	}

	if enc := getString(obj, "type"); enc != "" {
		e.Encryption = enc
	}

	if e.Network == "" {
		e.Network = "tcp"
	}

	return e
}

func parseSSJSON(obj map[string]interface{}) SubEntry {
	e := SubEntry{
		Protocol: "ss",
		Address:  getString(obj, "server"),
		Port:     getInt(obj, "server_port", 443),
		Method:   getString(obj, "method"),
		Password: getString(obj, "password"),
	}
	e.Remarks = getString(obj, "remarks")
	if e.Remarks == "" {
		e.Remarks = getString(obj, "ps")
	}
	return e
}

func parseURLList(data string) ([]SubEntry, error) {
	lines := strings.Split(data, "\n")
	var entries []SubEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e, err := parseURL(line)
		if err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no valid URLs found")
	}
	return entries, nil
}

func parseURL(rawURL string) (SubEntry, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return SubEntry{}, err
	}

	e := SubEntry{
		Protocol: u.Scheme,
		Port:     443,
	}

	switch u.Scheme {
	case "vless":
		parseVlessURL(u, &e)

	case "ss":
		if err := parseSSURL(u, &e); err != nil {
			return SubEntry{}, fmt.Errorf("parse ss url: %w", err)
		}

	case "vmess":
		parseVMessURL(u, &e)

	case "trojan":
		parseTrojanURL(u, &e)

	case "hysteria2", "hysteria", "hy2":
		parseHysteria2URL(u, &e)
		e.Protocol = "hysteria2"

	default:
		return SubEntry{}, fmt.Errorf("unsupported protocol: %s", u.Scheme)
	}

	return e, nil
}

func parseVlessURL(u *url.URL, e *SubEntry) {
	e.UUID = u.User.Username()

	host, portStr, err := net.SplitHostPort(u.Host)
	if err == nil {
		e.Address = host
		if p, pErr := strconv.Atoi(portStr); pErr == nil {
			e.Port = p
		}
	} else {
		e.Address = u.Host
	}

	q := u.Query()
	e.Flow = q.Get("flow")
	e.Network = q.Get("type")
	e.Security = q.Get("security")
	e.Path = q.Get("path")
	e.Host = q.Get("host")
	e.SNI = q.Get("sni")
	e.Fingerprint = q.Get("fp")
	e.PublicKey = q.Get("pbk")
	e.ShortID = firstNonEmpty(q.Get("sid"), q.Get("shortId"), q.Get("shortID"), q.Get("short_id"))
	e.ALPN = q.Get("alpn")
	e.ServiceName = q.Get("serviceName")
	e.SpiderX = q.Get("spx")
	e.XHTTPMode = q.Get("mode")

	if e.Network == "" {
		e.Network = "tcp"
	}
	e.Remarks = decodeFragment(u)

	if e.Security == "reality" && e.ShortID == "" {
		slog.Debug("REALITY URL has empty shortId (server may accept any)",
			"address", e.Address)
	}
}

// parseTrojanURL reads a trojan link: the password is the userinfo and transport
// / TLS parameters ride in the query the same way as vless.
func parseTrojanURL(u *url.URL, e *SubEntry) {
	e.Password = u.User.Username()

	host, portStr, err := net.SplitHostPort(u.Host)
	if err == nil {
		e.Address = host
		if p, pErr := strconv.Atoi(portStr); pErr == nil {
			e.Port = p
		}
	} else {
		e.Address = u.Host
	}

	q := u.Query()
	e.Network = firstNonEmpty(q.Get("type"), "tcp")
	e.Security = firstNonEmpty(q.Get("security"), "tls")
	e.Path = q.Get("path")
	e.Host = q.Get("host")
	e.SNI = q.Get("sni")
	e.Fingerprint = q.Get("fp")
	e.PublicKey = q.Get("pbk")
	e.ShortID = firstNonEmpty(q.Get("sid"), q.Get("shortId"), q.Get("shortID"), q.Get("short_id"))
	e.ALPN = q.Get("alpn")
	e.ServiceName = q.Get("serviceName")
	e.SpiderX = q.Get("spx")
	e.XHTTPMode = q.Get("mode")
	e.Remarks = decodeFragment(u)
}

func parseSSURL(u *url.URL, e *SubEntry) error {
	host, portStr, err := net.SplitHostPort(u.Host)
	if err == nil {
		e.Address = host
		if p, pErr := strconv.Atoi(portStr); pErr == nil {
			e.Port = p
		}
	} else {
		e.Address = u.Host
	}

	decoded, err := b64DecodeAnyPadding(u.User.Username())
	if err != nil {
		return fmt.Errorf("decode ss base64 userinfo: %w", err)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid ss userinfo format, expected method:password")
	}
	e.Method = parts[0]
	e.Password = parts[1]
	e.Remarks = decodeFragment(u)
	return nil
}

func parseVMessURL(u *url.URL, e *SubEntry) {
	// vmess://base64_encoded_json
	var b64 string
	if u.Opaque != "" {
		b64 = u.Opaque
	} else if u.Host != "" {
		// If there are //, Host contains the base64
		b64 = u.Host
		if u.User != nil {
			b64 = u.User.Username() + "@" + b64
		}
	}

	decoded, err := b64DecodeAnyPadding(b64)
	if err == nil {
		var vmessObj map[string]interface{}
		if err := json.Unmarshal(decoded, &vmessObj); err == nil {
			*e = parseVMessJSON(vmessObj)
		}
	}
}

func parseHysteria2URL(u *url.URL, e *SubEntry) {
	e.Password = u.User.Username()

	host, portStr, err := net.SplitHostPort(u.Host)
	if err == nil {
		e.Address = host
		if p, pErr := strconv.Atoi(portStr); pErr == nil {
			e.Port = p
		}
	} else {
		e.Address = u.Host
	}

	q := u.Query()
	e.Insecure = q.Get("insecure") == "1" || q.Get("insecure") == "true"
	e.Up = q.Get("up")
	e.Down = q.Get("down")
	e.SNI = q.Get("sni")
	e.ALPN = q.Get("alpn")
	e.Obfs = q.Get("obfs")
	e.ObfsPassword = q.Get("obfs-password")
	e.Congestion = q.Get("congestion")

	if e.Up != "" && !strings.Contains(strings.ToLower(e.Up), "bps") {
		e.Up = e.Up + " mbps"
	}
	if e.Down != "" && !strings.Contains(strings.ToLower(e.Down), "bps") {
		e.Down = e.Down + " mbps"
	}

	e.Remarks = decodeFragment(u)
}

// decodeFragment returns the URL fragment with percent-encoding removed,
// falling back to the raw fragment when unescaping fails (Q-1).
func decodeFragment(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(u.Fragment); err == nil {
		return decoded
	}
	return u.Fragment
}

// b64DecodeAnyPadding decodes base64 in either the standard or URL-safe
// alphabet, with or without padding — subscription sources use all four
// combinations (Q-1).
func b64DecodeAnyPadding(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if d, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return d, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func getString(obj map[string]interface{}, key string) string {
	if v, ok := obj[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func getInt(obj map[string]interface{}, key string, def int) int {
	if v, ok := obj[key]; ok {
		switch val := v.(type) {
		case float64:
			return int(val)
		case string:
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
		}
	}
	return def
}
