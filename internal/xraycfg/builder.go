package xraycfg

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// defaultTemplate is the template shipped inside the binary, so a release can be
// a single executable. A template.json next to the app still wins over it.
//
//go:embed template.json
var defaultTemplate []byte

// LoadTemplate reads the template at path, falling back to the embedded one when
// the file is absent.
func LoadTemplate(path string) (*XrayConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if os.IsNotExist(err) {
		data, err = defaultTemplate, nil
	}
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

// AddCatchAllRule appends a catch-all rule sending all tcp/udp traffic to the
// "proxy" outbound. Shared by MergeConfig and the benchmarker so the routing
// logic lives in exactly one place (A-3).
func AddCatchAllRule(routing json.RawMessage) json.RawMessage {
	var r map[string]interface{}
	if routing != nil {
		// Best-effort: a malformed routing block in the template degrades to just
		// the catch-all rule rather than failing the whole merge.
		_ = json.Unmarshal(routing, &r)
	}
	if r == nil {
		r = map[string]interface{}{}
	}
	rules, _ := r["rules"].([]interface{})
	rules = append(rules, map[string]interface{}{
		"type": "field", "outboundTag": "proxy", "network": "tcp,udp",
	})
	r["rules"] = rules
	modified, _ := json.Marshal(r)
	return modified
}

// MergeConfig injects the proxy outbound and catch-all route. Log is left unset
// on purpose: the caller sets the loglevel (Q-5), so MergeConfig no longer
// writes a value that would only be overwritten.
func MergeConfig(tc *XrayConfig, proxyOutbound json.RawMessage) *XrayConfig {
	outbounds := []json.RawMessage{proxyOutbound}
	for _, ob := range tc.Outbounds {
		outbounds = append(outbounds, ob)
	}

	return &XrayConfig{
		DNS:       tc.DNS,
		Inbounds:  tc.Inbounds,
		Outbounds: outbounds,
		Routing:   AddCatchAllRule(tc.Routing),
	}
}
