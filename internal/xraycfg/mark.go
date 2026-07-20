package xraycfg

import (
	"encoding/json"
	"fmt"
)

// DirectFwMark is the firewall mark xray stamps on sockets that must bypass the
// tunnel. In TUN mode the split default route claims every destination, so a
// freedom outbound's packets would otherwise re-enter the tun device they came
// from and loop until the connection dies. The routing layer pairs this mark
// with an ip rule sending marked packets down the physical path.
const DirectFwMark = 255

// MarkDirectOutbounds stamps DirectFwMark on every freedom outbound of a
// finished config. Only freedom needs it: blackhole never opens a socket, and
// the proxy outbounds already stay outside the tunnel through their per-server
// exception routes.
func MarkDirectOutbounds(raw json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(cfg["outbounds"]) == 0 {
		return raw, nil
	}

	var outbounds []map[string]json.RawMessage
	if err := json.Unmarshal(cfg["outbounds"], &outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}

	for _, ob := range outbounds {
		var protocol string
		if err := json.Unmarshal(ob["protocol"], &protocol); err != nil || protocol != "freedom" {
			continue
		}
		stream, err := withMark(ob["streamSettings"])
		if err != nil {
			return nil, err
		}
		ob["streamSettings"] = stream
	}

	encoded, err := json.Marshal(outbounds)
	if err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}
	cfg["outbounds"] = encoded

	return json.Marshal(cfg)
}

// withMark adds the mark to an outbound's sockopt, keeping whatever stream and
// sockopt settings the panel already configured.
func withMark(raw json.RawMessage) (json.RawMessage, error) {
	stream := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, fmt.Errorf("streamSettings: %w", err)
		}
	}

	sockopt := map[string]json.RawMessage{}
	if len(stream["sockopt"]) > 0 {
		if err := json.Unmarshal(stream["sockopt"], &sockopt); err != nil {
			return nil, fmt.Errorf("sockopt: %w", err)
		}
	}

	mark, err := json.Marshal(DirectFwMark)
	if err != nil {
		return nil, err
	}
	sockopt["mark"] = mark

	encodedSockopt, err := json.Marshal(sockopt)
	if err != nil {
		return nil, fmt.Errorf("sockopt: %w", err)
	}
	stream["sockopt"] = encodedSockopt

	return json.Marshal(stream)
}
