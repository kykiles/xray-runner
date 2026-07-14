package app

// Subscription/server selection flow, extracted from app.go (A-1).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/tui"
	"xray-runner/internal/ui"
	"xray-runner/internal/xraycfg"
)

func (a *App) resolveOutbound(ctx context.Context, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	// U-2: scripted selection bypasses every menu.
	if a.opts.Server != "" || a.opts.UseLast || a.opts.NonInteractive {
		return a.resolveOutboundAuto(subs)
	}

	// U-3: the TUI needs a terminal; without one, only scripted selection works.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("%w: нет терминала для интерактивного выбора — используйте --server, --last или --non-interactive", ErrSelection)
	}

	if len(subs) == 0 && (a.cfg.SubscriptionURL != "" || a.cfg.VlessURL != "") {
		return a.resolveLegacyOutbound(ctx, tc)
	}

	cb := tui.SubsCallbacks{
		Add:    addSubscription,
		Delete: subscription.RemoveSubscription,
		Reload: subscription.LoadSubscriptions,
		Mask:   func(raw string) string { return maskURL(raw, a.cfg.MaskCreds) },
	}

	for {
		newSubs, idx, action, err := tui.SelectSubscription(subs, cb)
		if err != nil {
			return nil, fmt.Errorf("TUI: %w", err)
		}
		subs = newSubs
		if action == tui.SubsQuit {
			// ErrUserQuit propagates up to main for a single clean exit (R-4).
			return nil, ErrUserQuit
		}
		ui.Success(fmt.Sprintf("Подписка: %s", subs[idx].Name))

		outbound, err := a.fetchAndSelectServer(ctx, subs[idx].URL, tc)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrUserQuit) {
				return nil, err
			}
			if !errors.Is(err, ErrSwitchSubscription) {
				ui.Error(err.Error())
			}
			continue
		}
		return outbound, nil
	}
}

func (a *App) resolveLegacyOutbound(ctx context.Context, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	if a.cfg.SubscriptionURL != "" {
		for {
			fmt.Println("📡 Загрузка подписки...")
			hwid := config.GetOrCreateHWID(a.cfg.HWID)
			entries, err := subscription.FetchWithHWID(a.cfg.SubscriptionURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
			if err != nil {
				return nil, fmt.Errorf("subscription: %w", err)
			}
			slog.Info("subscription loaded", "servers", len(entries))

			refresh := func() ([]subscription.SubEntry, error) {
				return subscription.FetchWithHWID(a.cfg.SubscriptionURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
			}
			selected, action, err := tui.SelectServer(ctx, entries, refresh, nil)
			if err != nil {
				return nil, fmt.Errorf("TUI: %w", err)
			}
			if action == tui.ServerQuit {
				return nil, ErrUserQuit
			}
			if selected == nil {
				continue
			}
			if err := selected.Validate(); err != nil {
				return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
			}
			selected.AllowInsecure = a.cfg.AllowInsecure
			a.printSubEntryDetails(selected)
			a.setServerEndpoint(selected)
			resolveServer(selected.Address)
			return subscription.BuildOutboundJSON(selected)
		}
	}

	u, err := url.Parse(a.cfg.VlessURL)
	if err != nil {
		return nil, fmt.Errorf("parse VLESS_URL: %w", err)
	}
	a.printURLDetails(u)
	a.serverHost = hostFromURL(u)
	if _, portStr, err := net.SplitHostPort(u.Host); err == nil {
		a.serverPort, _ = strconv.Atoi(portStr)
	}
	resolveServer(a.serverHost)

	switch u.Scheme {
	case "vless":
		ob, err := xraycfg.BuildVLESSOutbound(u)
		if err != nil {
			return nil, fmt.Errorf("build vless: %w", err)
		}
		return json.Marshal(ob)
	case "ss":
		ob, err := xraycfg.BuildSSOutbound(u)
		if err != nil {
			return nil, fmt.Errorf("build ss: %w", err)
		}
		return json.Marshal(ob)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (vless, ss)", u.Scheme)
	}
}

// resolveOutboundAuto picks a subscription and server without prompting (U-2):
// the subscription comes from the saved state (--last), the single saved entry,
// or SUBSCRIPTION_URL; the server from --server (1-based index or name match)
// or the saved state. Failures wrap ErrSelection for a distinct exit code.
func (a *App) resolveOutboundAuto(subs []subscription.NamedSubscription) (json.RawMessage, error) {
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
	a.setServerEndpoint(selected)
	a.rememberSelection(subURL, selected)
	resolveServer(selected.Address)
	return subscription.BuildOutboundJSON(selected)
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

// rememberSelection persists the choice for later --last runs (U-2).
func (a *App) rememberSelection(subURL string, e *subscription.SubEntry) {
	if err := saveLastState(LastState{
		SubscriptionURL: subURL,
		ServerRemarks:   e.Remarks,
		ServerAddress:   e.Address,
		ServerPort:      e.Port,
	}); err != nil {
		slog.Warn("failed to save last-server state", "error", err)
	}
}

// addSubscription validates and stores a subscription URL; it runs inside the
// TUI, so it must not print — problems come back as errors.
func addSubscription(rawURL string) error {
	// S-2: reject anything that isn't an http(s) subscription URL outright
	// instead of warning and saving it anyway.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("некорректный URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL должен начинаться с http:// или https:// (получено %q)", parsed.Scheme)
	}

	if err := subscription.SaveSubscription(rawURL); err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}
	return nil
}

func (a *App) fetchAndSelectServer(ctx context.Context, subURL string, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	ui.Progress("Загрузка подписки")
	hwid := config.GetOrCreateHWID(a.cfg.HWID)
	entries, err := subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	ui.ClearLine()
	if err != nil {
		return nil, fmt.Errorf("загрузка подписки: %w", err)
	}
	slog.Info("subscription loaded", "servers", len(entries))
	ui.Success(fmt.Sprintf("Загружено %d серверов", len(entries)))

	// A-4: refresh re-fetches with the same HWID headers as the initial load.
	refresh := func() ([]subscription.SubEntry, error) {
		return subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	}

	pb := NewProxyBenchmarker(tc, a.binary, 3, 8*time.Second, a.cfg.AllowInsecure)
	selected, action, err := tui.SelectServer(ctx, entries, refresh, pb.Run)
	if err != nil {
		return nil, fmt.Errorf("TUI: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	switch action {
	case tui.ServerQuit:
		// U-1: q/Ctrl+C exits the whole app, not just the screen.
		return nil, ErrUserQuit
	case tui.ServerBack:
		return nil, ErrSwitchSubscription
	}

	if err := selected.Validate(); err != nil {
		return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
	}
	selected.AllowInsecure = a.cfg.AllowInsecure
	a.printSubEntryDetails(selected)
	a.setServerEndpoint(selected)
	a.rememberSelection(subURL, selected)
	resolveServer(selected.Address)
	return subscription.BuildOutboundJSON(selected)
}

// setServerEndpoint records the selected server so the kill switch can allow
// xray's own traffic to it (H-1). hysteria2 uses UDP; everything else TCP.
func (a *App) setServerEndpoint(e *subscription.SubEntry) {
	a.serverHost = e.Address
	a.serverPort = e.Port
	a.serverUDP = e.Protocol == "hysteria2"
}

func hostFromURL(u *url.URL) string {
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return u.Host
	}
	return host
}

func resolveServer(host string) {
	fmt.Print("🔍 DNS-резолв сервера... ")
	addrs, err := net.LookupHost(host)
	if err != nil {
		fmt.Printf("❌ ОШИБКА: %v\n", err)
		slog.Error("dns resolve failed", "host", host, "error", err)
		return
	}
	if len(addrs) == 0 {
		fmt.Println("❌ Нет записей A/AAAA")
		slog.Warn("dns resolve returned no records", "host", host)
		return
	}
	fmt.Printf("✅ %s → %s\n", host, strings.Join(addrs, ", "))
	slog.Info("dns resolved", "host", host, "addrs", addrs)
}
