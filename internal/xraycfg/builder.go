package xraycfg

import (
	"encoding/json"
	"fmt"
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
	if err := validateTemplate(&cfg); err != nil {
		return nil, fmt.Errorf("template %s: %w", path, err)
	}
	return &cfg, nil
}

// validateTemplate enforces the template contract that the rest of the code
// silently assumed (X-2): routing must be parseable and every rule's
// outboundTag must exist among the outbounds. The proxy outbound is injected
// later at merge time, so it counts as valid here.
func validateTemplate(cfg *XrayConfig) error {
	if len(cfg.Routing) == 0 {
		return nil
	}

	var routing struct {
		Rules []struct {
			OutboundTag string `json:"outboundTag"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(cfg.Routing, &routing); err != nil {
		return fmt.Errorf("routing не парсится: %w", err)
	}

	tags := map[string]bool{"proxy": true}
	for _, ob := range cfg.Outbounds {
		var o struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(ob, &o); err != nil {
			return fmt.Errorf("outbound не парсится: %w", err)
		}
		if o.Tag != "" {
			tags[o.Tag] = true
		}
	}

	for _, r := range routing.Rules {
		if r.OutboundTag != "" && !tags[r.OutboundTag] {
			return fmt.Errorf("routing ссылается на несуществующий outboundTag %q", r.OutboundTag)
		}
	}
	return nil
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
