### Task 4: Modify `internal/app/app.go` with subscription selection flow

**Files:**
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `subscription.LoadSubscriptions()`, `subscription.SaveSubscription()`, `subscription.NamedSubscription`, `subscription.Fetch()`, `ui.*`
- Produces: modified `resolveOutbound` with subscription selection loop + `AddSubFlow` method

- [ ] **Step 1: Add new imports and replace methods in `internal/app/app.go`**

**Import change:** Add `"xray-runner/internal/ui"` to the import block. Current imports are:
```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)
```
Just add `"xray-runner/internal/ui"` after the existing imports.

**Method changes:**

Replace the existing `resolveOutbound` method (and any helper methods like `printSubEntryDetails`, `printURLDetails`, `maskIfNeeded`, `maskString`, etc. that are still there) — actually, you only need to REPLACE the `resolveOutbound` method AND ADD the new methods. Keep all other existing methods (printSubEntryDetails, printURLDetails, etc.) as they are.

Replace the `resolveOutbound` method with these 5 new methods:

```go
func (a *App) resolveOutbound(ctx context.Context) (json.RawMessage, error) {
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	if len(subs) == 0 {
		return a.resolveLegacyOutbound(ctx)
	}

	for {
		subURL := a.selectSubscription(subs)
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

		outbound, err := a.fetchAndSelectServer(ctx, subURL)
		if err != nil {
			ui.Error(err.Error())
			continue
		}
		return outbound, nil
	}
}

func (a *App) resolveLegacyOutbound(ctx context.Context) (json.RawMessage, error) {
	if a.cfg.SubscriptionURL != "" {
		fmt.Println("📡 Загрузка подписки...")
		entries, err := subscription.Fetch(a.cfg.SubscriptionURL)
		if err != nil {
			return nil, fmt.Errorf("subscription: %w", err)
		}
		slog.Info("subscription loaded", "servers", len(entries))

		selected := subscription.ShowMenu(entries, a.cfg.SubscriptionURL)
		if selected == nil {
			return nil, fmt.Errorf("subscription selection cancelled")
		}
		a.printSubEntryDetails(selected)
		resolveServer(selected.Address)
		return subscription.BuildOutboundJSON(selected)
	}

	u, err := url.Parse(a.cfg.VlessURL)
	if err != nil {
		return nil, fmt.Errorf("parse VLESS_URL: %w", err)
	}
	a.printURLDetails(u)
	resolveServer(hostFromURL(u))

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

func (a *App) selectSubscription(subs []subscription.NamedSubscription) string {
	for {
		ui.Title("Подписки")
		for i, s := range subs {
			ui.Item(i+1, fmt.Sprintf("%-30s  %s", s.Name, ui.Dim(s.URL)))
		}
		ui.Item(len(subs)+1, ui.Colored(ui.ColorGreen, "✚ Добавить новую"))
		ui.Divider()

		input, err := ui.StyledInput(fmt.Sprintf("Выберите [1-%d]", len(subs)+1))
		if err != nil {
			ui.Error("Ошибка ввода")
			continue
		}

		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > len(subs)+1 {
			ui.Error("Некорректный номер")
			continue
		}

		if idx == len(subs)+1 {
			return ""
		}

		ui.Success(fmt.Sprintf("Подписка: %s", subs[idx-1].Name))
		return subs[idx-1].URL
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

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		ui.Warn("URL должен начинаться с http:// или https://")
	}

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

func (a *App) fetchAndSelectServer(ctx context.Context, subURL string) (json.RawMessage, error) {
	ui.Progress("Загрузка подписки")
	entries, err := subscription.Fetch(subURL)
	ui.ClearLine()
	if err != nil {
		return nil, fmt.Errorf("загрузка подписки: %w", err)
	}
	slog.Info("subscription loaded", "servers", len(entries))
	ui.Success(fmt.Sprintf("Загружено %d серверов", len(entries)))

	selected := subscription.ShowMenu(entries, subURL)
	if selected == nil {
		return nil, fmt.Errorf("switch subscription")
	}

	a.printSubEntryDetails(selected)
	resolveServer(selected.Address)
	return subscription.BuildOutboundJSON(selected)
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Run tests**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./...
```
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/app/app.go
git commit -m "feat: add subscription selection flow with AddSubFlow"
```
