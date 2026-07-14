package app

// Subscription/server selection flow, extracted from app.go (A-1).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/ui"
	"xray-runner/internal/xraycfg"
)

func (a *App) resolveOutbound(ctx context.Context, tc *xraycfg.XrayConfig) (json.RawMessage, error) {
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	if len(subs) == 0 && (a.cfg.SubscriptionURL != "" || a.cfg.VlessURL != "") {
		return a.resolveLegacyOutbound(ctx, tc)
	}

	for {
		subURL, err := a.selectSubscription(subs)
		if err != nil {
			// ErrUserQuit propagates up to main for a single clean exit (R-4).
			return nil, err
		}
		if subURL == "" {
			if err := a.addSubFlow(); err != nil {
				ui.Error(err.Error())
				subs, _ = subscription.LoadSubscriptions()
				if len(subs) == 0 {
					continue
				}
			} else {
				subs, _ = subscription.LoadSubscriptions()
			}
			continue
		}

		outbound, err := a.fetchAndSelectServer(ctx, subURL, tc)
		if err != nil {
			if errors.Is(err, context.Canceled) {
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
			selected := subscription.ShowMenu(ctx, entries, refresh, nil)
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

// selectSubscription returns the chosen subscription URL, or "" to trigger the
// add-subscription flow. It returns ErrUserQuit when the user chooses to exit or
// interrupts (Ctrl+C), so main can exit cleanly instead of via os.Exit (R-4).
func (a *App) selectSubscription(subs []subscription.NamedSubscription) (string, error) {
	for {
		ui.ClearScreen()
		ui.Title("Список моих подписок")
		for i, s := range subs {
			ui.Item(i+1, fmt.Sprintf("%-30s  %s", s.Name, ui.Dim(maskURL(s.URL, a.cfg.MaskCreds))))
		}
		ui.Divider()

		prompt := fmt.Sprintf("[ + - добавить, 0 - выход, 1-%d - выбор подписки, d - удалить ]", len(subs))
		fmt.Printf("  %s▸%s %s ", ui.ColorCyan, ui.ColorReset, prompt)
		input, err := ui.ReadKey()
		fmt.Println()
		if err != nil {
			if errors.Is(err, ui.ErrInterrupted) {
				return "", ErrUserQuit
			}
			ui.Error("Ошибка ввода")
			continue
		}

		switch {
		case input == "+":
			return "", nil
		case strings.EqualFold(input, "d"):
			if len(subs) == 0 {
				ui.Error("Нет подписок для удаления")
				continue
			}
			fmt.Printf("  Введите номер для удаления [1-%d]: ", len(subs))
			delKey, err := ui.ReadKey()
			fmt.Println()
			if err != nil {
				if errors.Is(err, ui.ErrInterrupted) {
					return "", ErrUserQuit
				}
				continue
			}
			delIdx, err := strconv.Atoi(delKey)
			if err != nil || delIdx < 1 || delIdx > len(subs) {
				ui.Error("Некорректный номер")
				continue
			}
			if err := subscription.RemoveSubscription(delIdx - 1); err != nil {
				ui.Error(fmt.Sprintf("Ошибка удаления: %v", err))
				continue
			}
			ui.Success(fmt.Sprintf("Подписка «%s» удалена", subs[delIdx-1].Name))
			subs, _ = subscription.LoadSubscriptions()
			continue
		case input == "0":
			return "", ErrUserQuit
		default:
			idx, err := strconv.Atoi(input)
			if err != nil || idx < 1 || idx > len(subs) {
				ui.Error("Некорректный номер")
				continue
			}
			ui.Success(fmt.Sprintf("Подписка: %s", subs[idx-1].Name))
			return subs[idx-1].URL, nil
		}
	}
}

func (a *App) addSubFlow() error {
	ui.Title("Добавление подписки")

	rawURL, err := ui.StyledInput("Вставьте URL подписки")
	if err != nil {
		return fmt.Errorf("ввод отменён")
	}
	if rawURL == "" {
		return fmt.Errorf("URL не может быть пустым")
	}

	// S-2: reject anything that isn't an http(s) subscription URL outright
	// instead of warning and saving it anyway.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("некорректный URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL должен начинаться с http:// или https:// (получено %q)", parsed.Scheme)
	}
	if parsed.Scheme == "http" {
		ui.Warn("URL использует http без шифрования — предпочтительнее https")
	}

	ui.Warn("Проверка URL идёт напрямую, вне VPN-туннеля")
	ui.Progress("Проверка URL")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, headErr := client.Head(rawURL)
	ui.ClearLine()
	if headErr != nil {
		ui.Warn(fmt.Sprintf("Не удалось проверить URL: %v (всё равно сохраню)", headErr))
	} else {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			ui.Success("URL доступен")
		} else {
			ui.Warn(fmt.Sprintf("URL вернул HTTP %d (всё равно сохраню)", resp.StatusCode))
		}
	}

	if err := subscription.SaveSubscription(rawURL); err != nil {
		return fmt.Errorf("ошибка сохранения: %w", err)
	}

	ui.Success("Подписка добавлена")
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
	selected := subscription.ShowMenu(ctx, entries, refresh, pb.Run)
	if selected == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSwitchSubscription
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
