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
	"unicode"
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
	skipped := 0
	for i, item := range arr {
		e, err := parseJSONEntry(item)
		if err != nil {
			// The entry itself is not logged: it carries the UUID or password.
			slog.Debug("subscription entry skipped", "index", i, "error", err)
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	logSkipped(skipped, len(entries))
	if len(entries) == 0 {
		return nil, fmt.Errorf("no valid entries found in JSON")
	}
	return entries, nil
}

// logSkipped reports servers that failed to parse. Without it a subscription
// that half-parses looks identical to a short one, and the user has no way to
// tell that seven of their ten servers were dropped.
func logSkipped(skipped, kept int) {
	if skipped > 0 {
		slog.Warn("часть серверов подписки не разобрана", "пропущено", skipped, "загружено", kept)
	}
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
		Protocol:     getString(obj, "protocol"),
		Address:      getString(obj, "address"),
		Port:         getInt(obj, "port", 443),
		UUID:         getString(obj, "uuid"),
		Flow:         getString(obj, "flow"),
		Network:      getString(obj, "network"),
		Security:     getString(obj, "security"),
		Path:         getString(obj, "path"),
		Host:         getString(obj, "host"),
		SNI:          getString(obj, "sni"),
		Fingerprint:  getString(obj, "fp"),
		PublicKey:    getString(obj, "publicKey"),
		ShortID:      firstNonEmpty(getString(obj, "shortId"), getString(obj, "sid"), getString(obj, "shortID"), getString(obj, "short_id")),
		ALPN:         getString(obj, "alpn"),
		ServiceName:  getString(obj, "serviceName"),
		Method:       getString(obj, "method"),
		Password:     getString(obj, "password"),
		Up:           getString(obj, "up"),
		Down:         getString(obj, "down"),
		Obfs:         getString(obj, "obfs"),
		ObfsPassword: getString(obj, "obfs-password"),
		Congestion:   getString(obj, "congestion"),
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
	// Normalize every spelling to hysteria2 so the rest of the package sees one
	// form — RunBenchmark keys the UDP-only skip off it.
	if e.Protocol == "hy2" || e.Protocol == "hysteria" {
		e.Protocol = "hysteria2"
	}
	if e.Network == "" && e.Protocol != "hysteria2" {
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

	// A CDN link points "add" at an IP and carries the real hostname in "sni";
	// without it the handshake would use the address and fail.
	e.SNI = getString(obj, "sni")
	e.Fingerprint = getString(obj, "fp")

	// "scy" is the encryption; "type" is the header obfuscation (none/http/...)
	// and must not be mistaken for it — xray rejects security:"http".
	e.Encryption = firstNonEmpty(getString(obj, "scy"), getString(obj, "security"))

	if e.Network == "" {
		e.Network = "tcp"
	}
	// A v2rayN gRPC link keeps serviceName in "path", the mode in "type" and the
	// :authority in "host" (A14).
	if e.Network == "grpc" {
		e.ServiceName = e.Path
		e.GRPCMode = getString(obj, "type")
		e.Authority = e.Host
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
	skipped := 0

	for lineNo, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e, err := parseURL(line)
		if err != nil {
			// The line itself is not logged: it carries the UUID or password.
			slog.Debug("subscription line skipped", "line", lineNo, "error", err)
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	logSkipped(skipped, len(entries))
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
		if err := parseVMessURL(u, &e); err != nil {
			return SubEntry{}, fmt.Errorf("parse vmess url: %w", err)
		}

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
	e.Flow = qget(q, "flow")
	e.Network = qget(q, "type")
	e.Security = qget(q, "security")
	e.Path = qget(q, "path")
	e.Host = qget(q, "host")
	e.SNI = qget(q, "sni")
	e.Fingerprint = qget(q, "fp")
	e.PublicKey = qget(q, "pbk")
	e.ShortID = firstNonEmpty(qget(q, "sid"), qget(q, "shortId"), qget(q, "shortID"), qget(q, "short_id"))
	e.ALPN = qget(q, "alpn")
	e.ServiceName = qget(q, "serviceName")
	e.SpiderX = qget(q, "spx")
	// "mode" is the gRPC mode (gun/multi) on a grpc link and the xhttp mode on
	// any other: one query key, two different settings.
	if e.Network == "grpc" {
		e.GRPCMode = qget(q, "mode")
	} else {
		e.XHTTPMode = qget(q, "mode")
	}
	e.Authority = qget(q, "authority")

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
	e.Network = firstNonEmpty(qget(q, "type"), "tcp")
	e.Security = firstNonEmpty(qget(q, "security"), "tls")
	e.Path = qget(q, "path")
	e.Host = qget(q, "host")
	e.SNI = qget(q, "sni")
	e.Fingerprint = qget(q, "fp")
	e.PublicKey = qget(q, "pbk")
	e.ShortID = firstNonEmpty(qget(q, "sid"), qget(q, "shortId"), qget(q, "shortID"), qget(q, "short_id"))
	e.ALPN = qget(q, "alpn")
	e.ServiceName = qget(q, "serviceName")
	e.SpiderX = qget(q, "spx")
	// "mode" is the gRPC mode (gun/multi) on a grpc link and the xhttp mode on
	// any other: one query key, two different settings.
	if e.Network == "grpc" {
		e.GRPCMode = qget(q, "mode")
	} else {
		e.XHTTPMode = qget(q, "mode")
	}
	e.Authority = qget(q, "authority")
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

	// SIP002 allows either "method:password" in the clear or the same pair
	// base64-encoded in the username. url.Parse splits the plain form for us, so
	// a present password means we are looking at it.
	if pass, ok := u.User.Password(); ok {
		e.Method = u.User.Username()
		e.Password = pass
		e.Remarks = decodeFragment(u)
		return nil
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

func parseVMessURL(u *url.URL, e *SubEntry) error {
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
	if err != nil {
		return fmt.Errorf("decode vmess base64: %w", err)
	}
	var vmessObj map[string]interface{}
	if err := json.Unmarshal(decoded, &vmessObj); err != nil {
		return fmt.Errorf("decode vmess json: %w", err)
	}
	*e = parseVMessJSON(vmessObj)
	if e.Address == "" {
		return fmt.Errorf("vmess link has no address")
	}
	return nil
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
	e.Insecure = qget(q, "insecure") == "1" || qget(q, "insecure") == "true"
	e.Up = qget(q, "up")
	e.Down = qget(q, "down")
	e.SNI = qget(q, "sni")
	e.ALPN = qget(q, "alpn")
	e.Obfs = qget(q, "obfs")
	e.ObfsPassword = qget(q, "obfs-password")
	e.Congestion = qget(q, "congestion")

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
		return sanitize(decoded)
	}
	return sanitize(u.Fragment)
}

// sanitize drops control runes from a string that came off a subscription.
// Names and remarks are rendered to the terminal and written to the log, where
// an escape sequence would let the panel repaint the screen or forge log lines;
// lipgloss counts those sequences as zero-width, so truncation is no defence.
func sanitize(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// qget reads a query parameter that came off a subscription. url.Values already
// percent-decoded it, so this is the boundary where control runes must go.
func qget(q url.Values, key string) string {
	return sanitize(q.Get(key))
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
			return sanitize(s)
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
