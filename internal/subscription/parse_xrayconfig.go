package subscription

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Some panels return a subscription as an array of full Xray client configs
// (one per profile) instead of a URL list. The real servers live inside each
// config's proxy outbounds, so we extract them here. Structs below cover only
// the fields we surface as bare links.

type xrayConfig struct {
	Remarks   string            `json:"remarks"`
	Outbounds []json.RawMessage `json:"outbounds"`
	Routing   struct {
		Balancers []xrayBalancer `json:"balancers"`
	} `json:"routing"`
}

type xrayBalancer struct {
	Tag      string `json:"tag"`
	Strategy struct {
		Type string `json:"type"`
	} `json:"strategy"`
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
	// hysteria2 carries the server inline instead of a node list.
	Address string `json:"address"`
	Port    int    `json:"port"`
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
		MultiMode   bool   `json:"multiMode"`
		Authority   string `json:"authority"`
	} `json:"grpcSettings"`
	Hysteria *struct {
		Auth string `json:"auth"`
	} `json:"hysteriaSettings"`
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
// servers that repeat across profiles. It backs the flat view (--dump-links,
// scripted selection); the menu uses parseXrayConfigProfiles instead.
func parseXrayConfigArray(arr []json.RawMessage) ([]SubEntry, error) {
	profiles, err := parseXrayConfigProfiles(arr)
	if err != nil {
		return nil, err
	}

	return FlattenUnique(profiles), nil
}

// parseXrayConfigProfiles maps every config in the array to one profile, keeping
// its name, balancer and raw body. Deduplication is per profile: the same server
// legitimately appears in several profiles, and dropping it globally would empty
// the later ones.
func parseXrayConfigProfiles(arr []json.RawMessage) ([]Profile, error) {
	var profiles []Profile

	for _, item := range arr {
		var cfg xrayConfig
		if err := json.Unmarshal(item, &cfg); err != nil {
			continue
		}

		var entries []SubEntry
		seen := map[string]bool{}
		for _, raw := range cfg.Outbounds {
			var o xrayOutbound
			if err := json.Unmarshal(raw, &o); err != nil {
				continue
			}
			e, ok := outboundToEntry(&o)
			if !ok {
				continue
			}
			// Preserve the original outbound so the launcher can run it verbatim
			// instead of rebuilding it from the distilled fields above.
			e.RawOutbound = raw
			e.ProfileRaw = item
			key := entryKey(e)
			if seen[key] {
				continue
			}
			seen[key] = true
			entries = append(entries, e)
		}
		if len(entries) == 0 {
			continue
		}

		p := Profile{
			Name:    sanitize(cfg.Remarks),
			Entries: entries,
			Raw:     item,
		}
		// A balancer over a single outbound adds nothing — treat it as plain.
		if len(cfg.Routing.Balancers) > 0 && len(entries) > 1 {
			b := cfg.Routing.Balancers[0]
			p.Balancer = &BalancerInfo{Tag: b.Tag, Strategy: b.Strategy.Type}
		}
		profiles = append(profiles, p)
	}

	if len(profiles) == 0 {
		return nil, profileError(len(arr))
	}
	return profiles, nil
}

func entryKey(e SubEntry) string {
	return e.Protocol + "|" + e.Address + "|" + strconv.Itoa(e.Port) + "|" + e.UUID + e.Password
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
		vn.Address = sanitize(vn.Address)
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
		s.Address = sanitize(s.Address)
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

	case "hysteria", "hysteria2":
		addr := sanitize(o.Settings.Address)
		if addr == "" {
			return SubEntry{}, false
		}
		e := SubEntry{
			Protocol: "hysteria2",
			Address:  addr,
			Port:     o.Settings.Port,
			Remarks:  addr,
		}
		if o.Stream.Hysteria != nil {
			e.Password = o.Stream.Hysteria.Auth
		}
		applyStream(&e, &o.Stream)
		return e, true

	default:
		return SubEntry{}, false
	}
}

func applyStream(e *SubEntry, s *xrayStream) {
	e.Network = orDefault(s.Network, "tcp")
	e.Security = s.Security

	switch {
	case s.Reality != nil:
		e.SNI = sanitize(s.Reality.ServerName)
		e.PublicKey = sanitize(s.Reality.PublicKey)
		e.ShortID = sanitize(s.Reality.ShortID)
		e.Fingerprint = sanitize(s.Reality.Fingerprint)
	case s.TLS != nil:
		e.SNI = sanitize(s.TLS.ServerName)
		e.Fingerprint = sanitize(s.TLS.Fingerprint)
		e.ALPN = sanitize(strings.Join(s.TLS.ALPN, ","))
	}

	if s.WS != nil {
		e.Path = sanitize(s.WS.Path)
		e.Host = sanitize(s.WS.Headers.Host)
	}
	if s.GRPC != nil {
		e.ServiceName = sanitize(s.GRPC.ServiceName)
		if s.GRPC.MultiMode {
			e.GRPCMode = "multi"
		}
		e.Authority = sanitize(s.GRPC.Authority)
	}
}
