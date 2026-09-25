package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(p.Outbounds, &outbounds); err != nil {
		return fmt.Errorf("outbounds: %w", err)
	}
	if len(outbounds) == 0 || len(outbounds) > maxOutbounds {
		return fmt.Errorf("outbounds: %d, ожидается от 1 до %d", len(outbounds), maxOutbounds)
	}
	for i, ob := range outbounds {
		var protocol string
		if err := json.Unmarshal(ob["protocol"], &protocol); err != nil {
			return fmt.Errorf("outbound %d: нет protocol", i)
		}
		if !slices.Contains(outboundProtocols, protocol) {
			return fmt.Errorf("outbound %d: протокол %q службой не запускается", i, protocol)
		}
	}
	for name, part := range map[string]json.RawMessage{
		"outbounds": p.Outbounds, "routing": p.Routing, "dns": p.DNS,
		"observatory": p.Observatory, "burstObservatory": p.BurstObservatory, "fakedns": p.FakeDNS,
	} {
		if len(part) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(part, &v); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := checkValue(name, "", v); err != nil {
			return err
		}
	}
	return nil
}

// checkValue walks one part. key is the field the value sits under, "" for an
// array element.
func checkValue(path, key string, v any) error {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if forbiddenKey(k) {
				return fmt.Errorf("%s.%s: поле, называющее файл, службой не принимается", path, k)
			}
			if err := checkValue(path+"."+k, k, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range v {
			if err := checkValue(fmt.Sprintf("%s[%d]", path, i), key, child); err != nil {
				return err
			}
		}
	case string:
		return checkString(path, key, v)
	}
	return nil
}

// forbiddenKey is a field that names a file: certificateFile, keyFile,
// masterKeyLog and whatever the core adds in their manner.
func forbiddenKey(k string) bool {
	l := strings.ToLower(k)
	return strings.HasSuffix(l, "file") || strings.HasSuffix(l, "log") || strings.HasSuffix(l, "dir")
}

func checkString(path, key, s string) error {
	switch key {
	case "address", "redirect", "dest", "server":
		// A unix socket, by path or in the abstract namespace — on Windows a
		// path with a drive or backslashes: the core would connect to it with
		// the service's rights. No host name or address looks like one.
		if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "@") || strings.Contains(s, `\`) || drivePath(s) {
			return fmt.Errorf("%s: unix-сокет службой не принимается", path)
		}
	case "network":
		if s == "domainsocket" || s == "ds" {
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
		if json.Unmarshal(ob["protocol"], &protocol) != nil || protocol != "wireguard" {
			continue
		}
		settings := map[string]json.RawMessage{}
		if len(ob["settings"]) > 0 && string(ob["settings"]) != "null" {
			if err := json.Unmarshal(ob["settings"], &settings); err != nil {
				return nil, fmt.Errorf("wireguard settings: %w", err)
			}
		}
		settings["noKernelTun"] = json.RawMessage("true")
		enc, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		ob["settings"] = enc
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(outbounds)
}
