package xraycfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ClientParts are the pieces of a core config the interface may hand the
// service (H10): what to connect through and how to route. Everything else —
// the log and its files, the api, stats, inbounds, the core itself — is the
// service's to set, since the core runs with the service's rights, and a
// config field that names a file would have it read or write that file for
// whoever asked.
type ClientParts struct {
	Outbounds        json.RawMessage `json:"outbounds"`
	Routing          json.RawMessage `json:"routing,omitempty"`
	DNS              json.RawMessage `json:"dns,omitempty"`
	Observatory      json.RawMessage `json:"observatory,omitempty"`
	BurstObservatory json.RawMessage `json:"burstObservatory,omitempty"`
	FakeDNS          json.RawMessage `json:"fakedns,omitempty"`
}

// LogLevels are the core's log levels.
var LogLevels = []string{"debug", "info", "warning", "error", "none"}

// maxOutbounds bounds a request; a panel profile has a few dozen.
const maxOutbounds = 1024

// outboundProtocols are the outbounds the service runs. Anything else — a new
// protocol, or a typo — is refused rather than handed to a core with rights.
var outboundProtocols = []string{
	"vless", "vmess", "trojan", "shadowsocks", "hysteria", "hysteria2",
	"socks", "http", "wireguard", "freedom", "blackhole", "dns", "loopback",
}

// ExtractClientParts takes a finished config apart into what a request may
// carry. The rest is dropped; the service builds it anew.
func ExtractClientParts(raw json.RawMessage) (ClientParts, error) {
	var p ClientParts
	if err := json.Unmarshal(raw, &p); err != nil {
		return ClientParts{}, fmt.Errorf("config: %w", err)
	}
	return p, nil
}

// Validate refuses parts that would have the core touch the machine beyond
// the network: files named by path, unix sockets, geo lists from files of
// their own ("ext:"). The service checks every request with it.
func (p ClientParts) Validate() error {
	if len(p.Outbounds) == 0 {
		return errors.New("нет outbounds")
	}
	for name, part := range map[string]json.RawMessage{
		"outbounds": p.Outbounds, "routing": p.Routing, "dns": p.DNS,
		"observatory": p.Observatory, "burstObservatory": p.BurstObservatory, "fakedns": p.FakeDNS,
	} {
		if len(part) == 0 {
			continue
		}
		if err := checkPart(name, part); err != nil {
			return err
		}
	}
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(p.Outbounds, &outbounds); err != nil {
		return fmt.Errorf("outbounds: %w", err)
	}
	if len(outbounds) == 0 || len(outbounds) > maxOutbounds {
		return fmt.Errorf("outbounds: %d, ожидается от 1 до %d", len(outbounds), maxOutbounds)
	}
	for i, ob := range outbounds {
		var protocol string
		if err := json.Unmarshal(ob[fieldOf(ob, "protocol")], &protocol); err != nil {
			return fmt.Errorf("outbound %d: нет protocol", i)
		}
		if !slices.Contains(outboundProtocols, protocol) {
			return fmt.Errorf("outbound %d: протокол %q службой не запускается", i, protocol)
		}
	}
	return nil
}

// maxDepth bounds the nesting of a part; a panel profile goes a few levels deep.
const maxDepth = 64

// checkPart walks one part token by token rather than through a map: a map
// keeps only the last of two equal keys, while the core decodes both — the
// second into what the first left, so a nested object of the first survives
// the check unseen.
func checkPart(name string, raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := checkValue(dec, name, "", 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s: лишние данные после значения", name)
	}
	return nil
}

// checkValue walks the next value of dec. key is the field the value sits
// under, "" at the top; an array element sits under its array's key.
//
// Keys are matched the way the core matches them: it decodes with
// encoding/json, which takes a key for a field whatever its case and folds
// Unicode on top (the Kelvin sign is a k, the long s an s). So a key outside
// ASCII is refused outright, the names checked are compared folded, and two
// keys of one object that fold alike are refused: the core would read both
// into one field.
func checkValue(dec *json.Decoder, path, key string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("%s: вложенность глубже %d", path, maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	switch tok := tok.(type) {
	case json.Delim:
		if tok == '[' {
			for i := 0; dec.More(); i++ {
				if err := checkValue(dec, fmt.Sprintf("%s[%d]", path, i), key, depth+1); err != nil {
					return err
				}
			}
		} else {
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				k := kt.(string)
				if err := checkKey(path, key, k, seen); err != nil {
					return err
				}
				if err := checkValue(dec, path+"."+k, k, depth+1); err != nil {
					return err
				}
			}
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return fmt.Errorf("%s: %w", path, err)
		}
	case string:
		return checkString(path, key, tok)
	}
	return nil
}

// checkKey checks key k of an object that sits under parent; seen holds the
// object's keys so far, folded.
func checkKey(path, parent, k string, seen map[string]bool) error {
	for _, r := range k {
		if r >= utf8.RuneSelf {
			return fmt.Errorf("%s: ключ %+q не из ASCII службой не принимается", path, k)
		}
	}
	f := foldKey(k)
	if seen[f] {
		return fmt.Errorf("%s: ключ %q повторяется (с точностью до регистра)", path, k)
	}
	seen[f] = true
	if forbiddenKey(k) {
		return fmt.Errorf("%s.%s: поле, называющее файл, службой не принимается", path, k)
	}
	if sameKey(parent, "sockopt") && !slices.ContainsFunc(sockoptKeys, func(n string) bool { return sameKey(k, n) }) {
		return fmt.Errorf("%s.%s: эта опция сокета службой не принимается", path, k)
	}
	return nil
}

// foldKey is key as encoding/json compares it to a field name.
func foldKey(k string) string {
	return strings.Map(func(r rune) rune { return unicode.ToUpper(unicode.ToLower(r)) }, k)
}

// sameKey reports whether the core reads key k into the field name.
func sameKey(k, name string) bool {
	return foldKey(k) == foldKey(name)
}

// fieldOf is the key of m the core reads the field name from, "" if none.
// Validate leaves at most one.
func fieldOf(m map[string]json.RawMessage, name string) string {
	for k := range m {
		if sameKey(k, name) {
			return k
		}
	}
	return ""
}

// forbiddenKey is a field that names a file: certificateFile, keyFile,
// masterKeyLog and whatever the core adds in their manner.
func forbiddenKey(k string) bool {
	l := strings.ToLower(k)
	return strings.HasSuffix(l, "file") || strings.HasSuffix(l, "log") || strings.HasSuffix(l, "dir")
}

// sockoptKeys are the socket options the service lets a client set: the ones
// that only tune a connection it makes anyway. The core sets them with the
// service's CAP_NET_ADMIN and CAP_NET_RAW, so the rest is refused: mark would
// stamp the service's own marks — the path around the tunnel, the kill
// switch's pass — on whatever the client likes, tproxy binds to addresses not
// the host's, and customSockopt is any setsockopt at all.
//
// interface stays: panels pin outbounds to an adapter with it, and it only
// picks the device a socket leaves by — the freedom outbound already leaves
// past the tunnel, and the kill switch still judges the packets, which carry
// no mark of the service's.
var sockoptKeys = []string{
	"tcpFastOpen", "tcpFastOpenQueueLength", "tcpNoDelay", "domainStrategy", "dialerProxy",
	"acceptProxyProtocol", "tcpKeepAliveInterval", "tcpKeepAliveIdle", "tcpCongestion",
	"tcpWindowClamp", "tcpMaxSeg", "tcpUserTimeout", "tcpMptcp", "penetrate", "v6only",
	"interface", "addressPortStrategy", "happyEyeballs", "trustedXForwardedFor",
}

func checkString(path, key, s string) error {
	switch v := strings.ToLower(strings.TrimSpace(s)); {
	case sameKey(key, "address"), sameKey(key, "redirect"), sameKey(key, "dest"), sameKey(key, "server"):
		// A unix socket, by path or in the abstract namespace — on Windows a
		// path with a drive or backslashes: the core would connect to it with
		// the service's rights. No host name or address looks like one.
		if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "@") || strings.Contains(v, `\`) || drivePath(v) {
			return fmt.Errorf("%s: unix-сокет службой не принимается", path)
		}
	case sameKey(key, "network"):
		// The core takes the transport's name whatever its case.
		if v == "domainsocket" || v == "ds" {
			return fmt.Errorf("%s: domainsocket службой не принимается", path)
		}
	}
	// Geo lists come from the service's own databases; "ext:" names a file.
	l := strings.ToLower(s)
	if strings.HasPrefix(l, "ext:") || strings.HasPrefix(l, "ext-domain:") || strings.HasPrefix(l, "ext-ip:") {
		return fmt.Errorf("%s: списки из файлов (ext:) службой не принимаются", path)
	}
	return nil
}

// drivePath reports a Windows path: a drive letter, a colon, a slash.
func drivePath(s string) bool {
	return len(s) >= 3 && s[1] == ':' && (s[2] == '/' || s[2] == '\\') &&
		(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
}

// AssembleService builds the config the service runs from validated parts:
// its own inbounds and log level around the client's outbounds and routing.
// A wireguard outbound is kept in userspace: a kernel interface of its own
// would be one more change to the host that nothing takes back.
func AssembleService(p ClientParts, inbounds []Inbound, logLevel string) (json.RawMessage, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !slices.Contains(LogLevels, logLevel) {
		return nil, fmt.Errorf("уровень лога ядра %q неизвестен", logLevel)
	}
	outbounds, err := userspaceWireguard(p.Outbounds)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"log":       LogConfig{Loglevel: logLevel},
		"inbounds":  inbounds,
		"outbounds": outbounds,
	}
	for name, part := range map[string]json.RawMessage{
		"routing": p.Routing, "dns": p.DNS, "observatory": p.Observatory,
		"burstObservatory": p.BurstObservatory, "fakedns": p.FakeDNS,
	} {
		if len(part) > 0 && string(part) != "null" {
			cfg[name] = part
		}
	}
	return json.Marshal(cfg)
}

func userspaceWireguard(raw json.RawMessage) (json.RawMessage, error) {
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}
	changed := false
	for _, ob := range outbounds {
		var protocol string
		if json.Unmarshal(ob[fieldOf(ob, "protocol")], &protocol) != nil || protocol != "wireguard" {
			continue
		}
		key := fieldOf(ob, "settings")
		if key == "" {
			key = "settings"
		}
		settings := map[string]json.RawMessage{}
		if len(ob[key]) > 0 && string(ob[key]) != "null" {
			if err := json.Unmarshal(ob[key], &settings); err != nil {
				return nil, fmt.Errorf("wireguard settings: %w", err)
			}
		}
		// Every key the core would read as noKernelTun goes, not only this
		// spelling: a twin sorted after it would have the last word.
		for k := range settings {
			if sameKey(k, "noKernelTun") {
				delete(settings, k)
			}
		}
		settings["noKernelTun"] = json.RawMessage("true")
		enc, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		ob[key] = enc
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(outbounds)
}
