package subscription

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Some panels return a subscription as an array of full Xray client configs
// (one per profile) instead of a URL list. The real servers live inside each
// config's proxy outbounds, so we extract them here. Structs below cover only
// the fields we surface as bare links.

type xrayConfig struct {
	Outbounds []xrayOutbound `json:"outbounds"`
}

type xrayOutbound struct {
	Tag      string             `json:"tag"`
	Protocol string             `json:"protocol"`
	Settings xrayOutboundSettdo `json:"settings"`
	Stream   xrayStream         `json:"streamSettings"`
}

type xrayOutboundSettdo struct {
	Vnext   []xrayVNext  `json:"vnext"`   // vless / vmess
	Servers []xraySSNode `json:"servers"` // shadowsocks
}

type xrayVNext struct {
	Address string     `json:"address"`
	Port    int        `json:"port"`
	Users   []xrayUser `json:"users"`
}

type xrayUser struct {
	ID string `json:"id"`
	// Flow applies to vless; Security to vmess.
	Flow     string `json:"flow"`
	Security string `json:"security"`
}

type xraySSNode struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Method   string `json:"method"`
	Password string `json:"password"`
}

type xrayStream struct {
	Network  string `json:"network"`
	Security string `json:"security"`
	TLS      *struct {
		ServerName  string   `json:"serverName"`
		Fingerprint string   `json:"fingerprint"`
		ALPN        []string `json:"alpn"`
	} `json:"tlsSettings"`
	Reality *struct {
		ServerName  string `json:"serverName"`
		PublicKey   string `json:"publicKey"`
		ShortID     string `json:"shortId"`
		Fingerprint string `json:"fingerprint"`
	} `json:"realitySettings"`
	WS *struct {
		Path    string `json:"path"`
		Headers struct {
			Host string `json:"Host"`
		} `json:"headers"`
	} `json:"wsSettings"`
	GRPC *struct {
		ServiceName string `json:"serviceName"`
	} `json:"grpcSettings"`
}

// isXrayConfigArray reports whether the array's first element looks like a full
// Xray config (i.e. carries an "outbounds" list) rather than a flat server entry.
func isXrayConfigArray(arr []json.RawMessage) bool {
	if len(arr) == 0 {
		return false
	}
	var probe struct {
		Outbounds json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(arr[0], &probe); err != nil {
		return false
	}
	return len(probe.Outbounds) > 0
}

// parseXrayConfigArray extracts proxy outbounds from every config, deduplicating
// servers that repeat across profiles.
func parseXrayConfigArray(arr []json.RawMessage) ([]SubEntry, error) {
	var entries []SubEntry
	seen := map[string]bool{}

	for _, item := range arr {
		var cfg xrayConfig
		if err := json.Unmarshal(item, &cfg); err != nil {
			continue
		}
		for i := range cfg.Outbounds {
			e, ok := outboundToEntry(&cfg.Outbounds[i])
			if !ok {
				continue
			}
			key := e.Protocol + "|" + e.Address + "|" + strconv.Itoa(e.Port) + "|" + e.UUID + e.Password
			if seen[key] {
				continue
			}
			seen[key] = true
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no proxy outbounds found in xray config array")
	}
	return entries, nil
}

// outboundToEntry maps a single Xray outbound to a SubEntry. It returns ok=false
// for non-proxy outbounds (freedom, blackhole, dns) or malformed ones.
func outboundToEntry(o *xrayOutbound) (SubEntry, bool) {
	switch o.Protocol {
	case "vless", "vmess":
		if len(o.Settings.Vnext) == 0 || len(o.Settings.Vnext[0].Users) == 0 {
			return SubEntry{}, false
		}
		vn := o.Settings.Vnext[0]
		e := SubEntry{
			Protocol: o.Protocol,
			Address:  vn.Address,
			Port:     vn.Port,
			UUID:     vn.Users[0].ID,
			Flow:     vn.Users[0].Flow,
			Remarks:  vn.Address,
		}
		if o.Protocol == "vmess" {
			e.Encryption = vn.Users[0].Security
		}
		applyStream(&e, &o.Stream)
		if e.Address == "" {
			return SubEntry{}, false
		}
		return e, true

	case "shadowsocks":
		if len(o.Settings.Servers) == 0 {
			return SubEntry{}, false
		}
		s := o.Settings.Servers[0]
		if s.Address == "" {
			return SubEntry{}, false
		}
		return SubEntry{
			Protocol: "ss",
			Address:  s.Address,
			Port:     s.Port,
			Method:   s.Method,
			Password: s.Password,
			Remarks:  s.Address,
		}, true

	default:
		return SubEntry{}, false
	}
}

func applyStream(e *SubEntry, s *xrayStream) {
	e.Network = orDefault(s.Network, "tcp")
	e.Security = s.Security

	switch {
	case s.Reality != nil:
		e.SNI = s.Reality.ServerName
		e.PublicKey = s.Reality.PublicKey
		e.ShortID = s.Reality.ShortID
		e.Fingerprint = s.Reality.Fingerprint
	case s.TLS != nil:
		e.SNI = s.TLS.ServerName
		e.Fingerprint = s.TLS.Fingerprint
		e.ALPN = strings.Join(s.TLS.ALPN, ",")
	}

	if s.WS != nil {
		e.Path = s.WS.Path
		e.Host = s.WS.Headers.Host
	}
	if s.GRPC != nil {
		e.ServiceName = s.GRPC.ServiceName
	}
}
