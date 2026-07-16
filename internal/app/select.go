package app

// Subscription/server selection flow, extracted from app.go (A-1).

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// resolveScriptedTarget picks a subscription and server without prompting (U-2):
// the subscription comes from the saved state (--last), the single saved entry,
// or SUBSCRIPTION_URL; the server from --server (1-based index or name match)
// or the saved state. Failures wrap ErrSelection for a distinct exit code.
// Profiles are flattened here — a script names one server, not a balancer group.
func (a *App) resolveScriptedTarget() (*target, error) {
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}
	state, stateErr := loadLastState()

	subURL := ""
	switch {
	case a.opts.UseLast:
		if stateErr != nil {
			return nil, fmt.Errorf("%w: --last: нет сохранённого выбора (%v)", ErrSelection, stateErr)
		}
		subURL = state.SubscriptionURL
	case len(subs) == 1:
		subURL = subs[0].URL
	case a.cfg.SubscriptionURL != "":
		subURL = a.cfg.SubscriptionURL
	case stateErr == nil:
		subURL = state.SubscriptionURL
	default:
		return nil, fmt.Errorf("%w: несколько подписок и нет сохранённого выбора — укажите --last или оставьте одну подписку", ErrSelection)
	}

	hwid := config.GetOrCreateHWID(a.cfg.HWID)
	entries, err := subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	if err != nil {
		return nil, fmt.Errorf("загрузка подписки: %w", err)
	}
	slog.Info("subscription loaded", "servers", len(entries))

	selected, err := pickEntry(entries, a.opts.Server, state, a.opts.UseLast)
	if err != nil {
		return nil, err
	}

	if err := selected.Validate(); err != nil {
		return nil, fmt.Errorf("%w: выбранный сервер невалиден: %v", ErrSelection, err)
	}
	selected.AllowInsecure = a.cfg.AllowInsecure
	a.printSubEntryDetails(selected)
	a.rememberSelection(subURL, selected)
	return &target{subURL: subURL, entry: selected}, nil
}

// migrateLegacyURL seeds an empty list from .env: SUBSCRIPTION_URL as a
// subscription, VLESS_URL as a bare link. Both are one-time imports — once the
// list has entries, .env is not consulted again.
func (a *App) migrateLegacyURL() error {
	if len(a.nav.subs) > 0 {
		return nil
	}
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	if len(subs) > 0 {
		a.nav.subs = subs
		return nil
	}

	legacy := []struct{ name, value string }{
		{"SUBSCRIPTION_URL", a.cfg.SubscriptionURL},
		{"VLESS_URL", a.cfg.VlessURL},
	}
	imported := false
	for _, l := range legacy {
		if l.value == "" {
			continue
		}
		if err := addSubscription(l.value); err != nil {
			return fmt.Errorf("%s: %w", l.name, err)
		}
		slog.Info("migrated legacy url from .env into the subscription list", "var", l.name)
		imported = true
	}
	if !imported {
		return nil // the menu will prompt for the first subscription
	}

	subs, err = subscription.LoadSubscriptions()
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	a.nav.subs = subs
	return nil
}

// pickEntry resolves --server (1-based index, exact or unique substring match
// on remarks/address) or falls back to the saved last server.
func pickEntry(entries []subscription.SubEntry, sel string, state *LastState, useLast bool) (*subscription.SubEntry, error) {
	if sel != "" {
		if idx, err := strconv.Atoi(sel); err == nil {
			if idx < 1 || idx > len(entries) {
				return nil, fmt.Errorf("%w: --server %d вне диапазона 1-%d", ErrSelection, idx, len(entries))
			}
			return &entries[idx-1], nil
		}
		var matches []*subscription.SubEntry
		for i := range entries {
			if strings.EqualFold(entries[i].Remarks, sel) {
				return &entries[i], nil
			}
			if strings.Contains(strings.ToLower(entries[i].Remarks), strings.ToLower(sel)) ||
				strings.Contains(strings.ToLower(entries[i].Address), strings.ToLower(sel)) {
				matches = append(matches, &entries[i])
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return nil, fmt.Errorf("%w: сервер %q не найден в подписке (%d серверов)", ErrSelection, sel, len(entries))
		default:
			return nil, fmt.Errorf("%w: %q совпадает с %d серверами — уточните имя или используйте индекс", ErrSelection, sel, len(matches))
		}
	}

	if useLast || state != nil {
		if state == nil {
			return nil, fmt.Errorf("%w: нет сохранённого сервера", ErrSelection)
		}
		for i := range entries {
			if entries[i].Address == state.ServerAddress && entries[i].Port == state.ServerPort {
				return &entries[i], nil
			}
		}
		for i := range entries {
			if entries[i].Remarks != "" && entries[i].Remarks == state.ServerRemarks {
				return &entries[i], nil
			}
		}
		return nil, fmt.Errorf("%w: сохранённый сервер %s (%s:%d) больше не в подписке", ErrSelection, state.ServerRemarks, state.ServerAddress, state.ServerPort)
	}
	return nil, fmt.Errorf("%w: не указан сервер (--server или --last)", ErrSelection)
}

// rememberSelection persists the choice for later --last runs (U-2). It edits
// the saved state rather than replacing it, so the remembered mode survives.
func (a *App) rememberSelection(subURL string, e *subscription.SubEntry) {
	if err := updateState(func(s *LastState) {
		s.SubscriptionURL = subURL
		s.ServerRemarks = e.Remarks
		s.ServerAddress = e.Address
		s.ServerPort = e.Port
	}); err != nil {
		slog.Warn("failed to save last-server state", "error", err)
	}
}

// addSubscription validates and stores a subscription URL; it runs inside the
// TUI, so it must not print — problems come back as errors.
func addSubscription(rawURL string) error {
	if err := validateSubscriptionInput(rawURL); err != nil {
		return err
	}
	if err := subscription.SaveSubscription(rawURL); err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}
	return nil
}

// validateSubscriptionInput accepts the two things the list can hold: an
// http(s) subscription URL, or a bare link to a single server. S-2: reject
// anything else outright instead of saving it and failing at connect time.
func validateSubscriptionInput(rawURL string) error {
	if subscription.IsBareLink(rawURL) {
		e, err := subscription.ParseBareLink(rawURL)
		if err != nil {
			return err
		}
		// A link the parser accepts can still be missing the fields xray needs,
		// so it is checked here rather than after it is saved.
		if err := e.Validate(); err != nil {
			return fmt.Errorf("ссылка неполная: %w", err)
		}
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return nil
	}
	return fmt.Errorf("нужна ссылка на подписку (http/https) или на сервер (vless/vmess/ss/trojan/hysteria2), получено %q", rawURL)
}

// setServerEndpoint records the selected server so the kill switch can allow
// xray's own traffic to it (H-1). hysteria2 uses UDP; everything else TCP.
func (a *App) setServerEndpoint(e *subscription.SubEntry) {
	a.serverHost = e.Address
	a.serverPort = e.Port
	a.serverUDP = e.Protocol == "hysteria2"
}

// resolveServer is diagnostic only — it never blocks the connection, so its
// outcome belongs in the log rather than on the screen.
func resolveServer(host string) {
	addrs, err := net.LookupHost(host)
	if err != nil {
		slog.Error("dns resolve failed", "host", host, "error", err)
		return
	}
	if len(addrs) == 0 {
		slog.Warn("dns resolve returned no records", "host", host)
		return
	}
	slog.Info("dns resolved", "host", host, "addrs", addrs)
}
