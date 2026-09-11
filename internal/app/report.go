package app

// User-facing summaries and credential masking, extracted from app.go (A-1).

import (
	"fmt"
	"net/url"

	"xray-runner/internal/subscription"
	"xray-runner/internal/ui"
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
	// A16: up to 8 characters a partial mask shows the value whole (4+4 of 8),
	// and a one-character sid from a subscription panicked on s[:2].
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
