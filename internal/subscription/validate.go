package subscription

import (
	"fmt"

	"xray-runner/internal/xraycfg"
)

// Validate checks that a subscription entry has the fields its protocol needs
// before it reaches BuildOutboundJSON, so bad panel data fails with a clear
// message instead of a cryptic xray crash after several retries (Q-4).
func (e *SubEntry) Validate() error {
	if e.Address == "" {
		return fmt.Errorf("пустой адрес сервера")
	}
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("порт %d вне диапазона 1..65535", e.Port)
	}

	switch e.Protocol {
	case "vless", "vmess":
		if !isUUID(e.UUID) {
			return fmt.Errorf("некорректный UUID для %s", e.Protocol)
		}
	case "ss":
		if e.Method == "" || e.Password == "" {
			return fmt.Errorf("ss требует method и password")
		}
	case "hysteria2", "hysteria":
		if e.Password == "" {
			return fmt.Errorf("hysteria2 требует password")
		}
	case "trojan":
		if e.Password == "" {
			return fmt.Errorf("trojan требует password")
		}
	default:
		return fmt.Errorf("неподдерживаемый протокол: %s", e.Protocol)
	}

	// The same rules the builders apply, so the list marks the entry and refuses
	// to connect instead of failing (or, before A07, silently losing
	// protection) at build time — the security value (A07) and the
	// transport/security/flow combination (A18).
	switch e.Protocol {
	case "vless", "vmess", "trojan":
		if err := xraycfg.CheckStream(e.Network, e.Security, e.Flow); err != nil {
			return err
		}
	}
	return nil
}

// isUUID reports whether s has the canonical 8-4-4-4-12 hex UUID form.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(r) {
				return false
			}
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
