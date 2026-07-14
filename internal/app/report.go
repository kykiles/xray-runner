package app

// User-facing summaries and credential masking, extracted from app.go (A-1).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"xray-runner/internal/subscription"
	"xray-runner/internal/ui"
	"xray-runner/internal/xraycfg"
)

func (a *App) printSubEntryDetails(e *subscription.SubEntry) {
	fmt.Println("── Выбранный сервер ─────────────────────")
	fmt.Printf("  Протокол:  %s\n", e.Protocol)
	fmt.Printf("  Сервер:    %s\n", e.Address)
	fmt.Printf("  Порт:      %d\n", e.Port)
	if e.UUID != "" {
		fmt.Printf("  UUID:      %s\n", a.maskIfNeeded(e.UUID))
	}
	fmt.Printf("  Transport: %s\n", e.Network)
	if e.Security != "" {
		fmt.Printf("  Security:  %s\n", e.Security)
	}
	if e.Remarks != "" {
		fmt.Printf("  Заметка:   %s\n", e.Remarks)
	}
	if e.ShortID != "" {
		fmt.Printf("  ShortID:   %s\n", a.maskIfNeeded(e.ShortID))
	}
	// S-1: warn when the subscription requested skipping TLS verification.
	if e.Insecure {
		if a.cfg.AllowInsecure {
			ui.Warn("Сервер запросил insecure — проверка TLS-сертификата ОТКЛЮЧЕНА (ALLOW_INSECURE=true)")
		} else {
			ui.Warn("Сервер запросил insecure — флаг проигнорирован (задайте ALLOW_INSECURE=true, чтобы разрешить)")
		}
	}
	fmt.Println("─────────────────────────────────────────")
}

func (a *App) printURLDetails(u *url.URL) {
	fmt.Println("── URL ──────────────────────────────────")
	fmt.Printf("  Протокол:  %s\n", u.Scheme)
	host, port, _ := net.SplitHostPort(u.Host)
	fmt.Printf("  Сервер:    %s\n", host)
	fmt.Printf("  Порт:      %s\n", port)
	if u.Scheme == "vless" {
		q := u.Query()
		fmt.Printf("  UUID:      %s\n", a.maskIfNeeded(u.User.Username()))
		printParam(q, "type", "Transport")
		printParam(q, "security", "Security")
		printParam(q, "sni", "SNI")
		printParam(q, "fp", "Fingerprint")
		if v := q.Get("pbk"); v != "" {
			fmt.Printf("  PublicKey: %s\n", a.maskIfNeeded(v))
		}
		if v := q.Get("sid"); v != "" {
			fmt.Printf("  ShortID:   %s\n", a.maskIfNeeded(v))
		}
		printParam(q, "flow", "Flow")
		printParam(q, "host", "Host")
		printParam(q, "path", "Path")
		printParam(q, "alpn", "ALPN")
	}
	fmt.Println("─────────────────────────────────────────")
}

func printParam(q url.Values, key, label string) {
	if v := q.Get(key); v != "" {
		fmt.Printf("  %-10s %s\n", label+":", v)
	}
}

func (a *App) maskIfNeeded(s string) string {
	return maskString(s, a.cfg.MaskCreds)
}

// maskURL hides the personal token in a subscription URL, keeping only the
// host and the first few path characters for recognizability (S-3).
func maskURL(raw string, mask bool) string {
	if !mask || raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return maskString(raw, true)
	}
	path := u.Path
	if len(path) > 6 {
		path = path[:6] + "…"
	} else if path != "" {
		path += "…"
	}
	return u.Scheme + "://" + u.Host + path
}

func maskString(s string, mask bool) string {
	if !mask || s == "" {
		return s
	}
	if len(s) <= 8 {
		return s[:2] + "..." + s[len(s)-2:]
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func printConfigSummary(cfg *xraycfg.XrayConfig, mode string) {
	if len(cfg.Outbounds) == 0 {
		return
	}
	var proto struct {
		Protocol string                  `json:"protocol"`
		Settings json.RawMessage         `json:"settings"`
		Stream   *xraycfg.StreamSettings `json:"streamSettings,omitempty"`
	}
	if err := json.Unmarshal(cfg.Outbounds[0], &proto); err != nil {
		return
	}
	fmt.Printf("📡 Outbound: %s", proto.Protocol)
	if proto.Stream != nil {
		if proto.Stream.Network != "" {
			fmt.Printf(" | transport: %s", proto.Stream.Network)
		}
		if proto.Stream.Security != "" {
			fmt.Printf(" | security: %s", proto.Stream.Security)
		}
	}
	if proto.Settings != nil {
		var addr struct {
			VNext []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"vnext,omitempty"`
			Servers []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"servers,omitempty"`
		}
		if err := json.Unmarshal(proto.Settings, &addr); err == nil {
			if len(addr.VNext) > 0 {
				fmt.Printf(" | server: %s:%d", addr.VNext[0].Address, addr.VNext[0].Port)
			} else if len(addr.Servers) > 0 {
				fmt.Printf(" | server: %s:%d", addr.Servers[0].Address, addr.Servers[0].Port)
			}
		}
	}
	fmt.Printf(" | mode: %s\n", mode)
}

func printRoutingRules(cfg *xraycfg.XrayConfig) {
	var routing map[string]interface{}
	if cfg.Routing != nil {
		json.Unmarshal(cfg.Routing, &routing)
	}
	if routing == nil {
		return
	}
	rules, _ := routing["rules"].([]interface{})
	fmt.Printf("  Маршрутов: %d\n", len(rules))
	for i, r := range rules {
		if rule, ok := r.(map[string]interface{}); ok {
			tag, _ := rule["outboundTag"].(string)
			domains, _ := rule["domain"].([]interface{})
			ips, _ := rule["ip"].([]interface{})
			network, _ := rule["network"].(string)
			var parts []string
			if len(domains) > 0 {
				parts = append(parts, fmt.Sprintf("%d доменов", len(domains)))
			}
			if len(ips) > 0 {
				parts = append(parts, fmt.Sprintf("%d IP-сетей", len(ips)))
			}
			if network != "" {
				parts = append(parts, network)
			}
			desc := strings.Join(parts, ", ")
			if desc == "" {
				desc = "catch-all"
			}
			fmt.Printf("    %d. → %-7s  %s\n", i+1, tag, desc)
		}
	}
}
