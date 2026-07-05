package xraycfg

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func LoadTemplate(path string) (*XrayConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var cfg XrayConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func MergeConfig(tc *XrayConfig, proxyOutbound json.RawMessage) *XrayConfig {
	outbounds := []json.RawMessage{proxyOutbound}
	for _, ob := range tc.Outbounds {
		outbounds = append(outbounds, ob)
	}

	var routing map[string]interface{}
	if tc.Routing != nil {
		json.Unmarshal(tc.Routing, &routing)
	}
	if routing == nil {
		routing = map[string]interface{}{}
	}
	rules, _ := routing["rules"].([]interface{})
	rules = append(rules, map[string]interface{}{
		"type": "field", "outboundTag": "proxy", "network": "tcp,udp",
	})
	routing["rules"] = rules
	modifiedRouting, _ := json.Marshal(routing)

	return &XrayConfig{
		Log: &LogConfig{Loglevel: "warning"},
		DNS: tc.DNS,
		Inbounds: tc.Inbounds,
		Outbounds: outbounds,
		Routing: modifiedRouting,
	}
}
