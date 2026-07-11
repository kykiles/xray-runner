# CLI Terminal Enhancement — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `subscriptions.txt` management, professional ANSI-styled CLI menus, and an interactive startup flow (select subscription → select server → run Xray).

**Architecture:** New `internal/ui/` package provides ANSI-styled output functions with zero dependencies. New `internal/subscription/store.go` manages `subscriptions.txt`. Existing `menu.go` and `app.go` are enhanced to use the new UI and support multi-subscription flow.

**Tech Stack:** Go 1.21+, stdlib only (no new dependencies).

## Global Constraints

- No new external dependencies beyond existing `github.com/joho/godotenv`
- Go 1.21 minimum
- No CGO
- Files named in snake_case.go
- `subscriptions.txt` lives in process working directory
- Fallback to `.env`'s `SUBSCRIPTION_URL` / `VLESS_URL` when `subscriptions.txt` doesn't exist

---

### Task 1: Create `internal/ui/` package

**Files:**
- Create: `internal/ui/style.go`
- Create: `internal/ui/print.go`
- Create: `internal/ui/input.go`

**Interfaces:**
- Produces: `ui.Bold(text)`, `ui.Dim(text)`, `ui.Colored(color, text)`, `ui.Title(text)`, `ui.Item(i, text, tags...)`, `ui.Success(text)`, `ui.Error(text)`, `ui.Warn(text)`, `ui.Progress(text)`, `ui.ClearLine()`, `ui.Divider()`, `ui.StyledInput(prompt) (string, error)`

- [ ] **Step 1: Create `internal/ui/style.go`**

```go
package ui

const (
	ColorCyan   = "\033[36m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorDim    = "\033[2m"
	ColorReset  = "\033[0m"
)

func Bold(text string) string {
	return "\033[1m" + text + ColorReset
}

func Dim(text string) string {
	return ColorDim + text + ColorReset
}

func Colored(color, text string) string {
	return color + text + ColorReset
}
```

- [ ] **Step 2: Create `internal/ui/print.go`**

```go
package ui

import (
	"fmt"
	"strings"
)

func Title(text string) {
	fmt.Println()
	fmt.Printf("  %s%s%s %s\n", ColorCyan, Bold("──"), ColorReset, text)
	fmt.Printf("  %s%s%s\n", ColorCyan, strings.Repeat("─", 44), ColorReset)
}

func Item(i int, text string, tags ...string) {
	tagStr := ""
	for _, t := range tags {
		tagStr += "  " + Dim(t)
	}
	fmt.Printf("  %s%2d.%s  %s%s\n", ColorCyan, i, ColorReset, text, tagStr)
}

func Success(text string) {
	fmt.Printf("  %s✅%s %s\n", ColorGreen, ColorReset, text)
}

func Error(text string) {
	fmt.Printf("  %s❌%s %s\n", ColorRed, ColorReset, text)
}

func Warn(text string) {
	fmt.Printf("  %s⚠️%s %s\n", ColorYellow, ColorReset, text)
}

func Progress(text string) {
	fmt.Printf("  ⏳ %s...", text)
}

func ClearLine() {
	fmt.Print("\033[2K\r")
}

func Divider() {
	fmt.Printf("  %s─────────────────────────────────────────────%s\n", ColorDim, ColorReset)
}
```

- [ ] **Step 3: Create `internal/ui/input.go`**

```go
package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func StyledInput(prompt string) (string, error) {
	fmt.Printf("  %s▸%s %s: ", ColorCyan, ColorReset, prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}
```

- [ ] **Step 4: Verify compilation**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build ./internal/ui/
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/
git commit -m "feat: add ANSI-styled UI package (internal/ui)"
```

---

### Task 2: Create `internal/subscription/store.go` + tests

**Files:**
- Create: `internal/subscription/store.go`
- Create: `internal/subscription/store_test.go`

**Interfaces:**
- Produces: `NamedSubscription{URL, Name}`, `LoadSubscriptions() ([]NamedSubscription, error)`, `SaveSubscription(rawURL string) error`

- [ ] **Step 1: Write the failing test**

```go
// internal/subscription/store_test.go
package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSubscriptions_FileNotExist(t *testing.T) {
	// Use a non-existent file path by changing wd temporarily
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "nonexistent.txt")
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions on missing file: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected empty slice, got %d items", len(subs))
	}
}

func TestSaveAndLoadSubscription(t *testing.T) {
	dir := t.TempDir()
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(dir, "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	url := "https://example.com/sub"
	if err := SaveSubscription(url); err != nil {
		t.Fatalf("SaveSubscription: %v", err)
	}

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}
	if subs[0].URL != url {
		t.Errorf("URL = %q, want %q", subs[0].URL, url)
	}
	if subs[0].Name == "" {
		t.Error("expected non-empty Name")
	}
}

func TestLoadSubscriptions_WithComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "# My VPN\nhttps://vpn.example.com/sub\n\n# Backup\nhttps://backup.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "My VPN" {
		t.Errorf("Name[0] = %q, want %q", subs[0].Name, "My VPN")
	}
	if subs[1].Name != "Backup" {
		t.Errorf("Name[1] = %q, want %q", subs[1].Name, "Backup")
	}
}

func TestLoadSubscriptions_WithoutComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "https://vpn.example.com/sub\nhttps://other.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "vpn.example.com" {
		t.Errorf("Name[0] = %q, want hostname", subs[0].Name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -run TestLoad -v
```
Expected: FAIL (package not found or undefined).

- [ ] **Step 3: Write minimal implementation**

```go
// internal/subscription/store.go
package subscription

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type NamedSubscription struct {
	URL  string
	Name string
}

var subscriptionsFile = "subscriptions.txt"

func LoadSubscriptions() ([]NamedSubscription, error) {
	f, err := os.Open(subscriptionsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open subscriptions: %w", err)
	}
	defer f.Close()

	var subs []NamedSubscription
	var pendingComment string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			pendingComment = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		name := pendingComment
		if name == "" {
			if u, err := url.Parse(line); err == nil {
				name = u.Host
			} else {
				name = line
			}
		}
		subs = append(subs, NamedSubscription{URL: line, Name: name})
		pendingComment = ""
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan subscriptions: %w", err)
	}
	return subs, nil
}

func SaveSubscription(rawURL string) error {
	f, err := os.OpenFile(subscriptionsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("save subscription: %w", err)
	}
	defer f.Close()

	// Check duplicate
	subs, _ := LoadSubscriptions()
	for _, s := range subs {
		if s.URL == rawURL {
			return nil
		}
	}

	if _, err := fmt.Fprintln(f, rawURL); err != nil {
		return fmt.Errorf("write subscription: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -run TestLoad -v
```
Expected: all PASS.

- [ ] **Step 5: Run all existing tests to ensure no regression**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/subscription/store.go internal/subscription/store_test.go
git commit -m "feat: add subscription persistence (store.go)"
```

---

### Task 3: Enhance `internal/subscription/menu.go`

**Files:**
- Modify: `internal/subscription/menu.go`

**Interfaces:**
- Consumes: `ui.*` from Task 1
- Consumes: `NamedSubscription`, `LoadSubscriptions`, `SaveSubscription` from Task 2
- Produces: `ShowMenu(entries []SubEntry, subURL string) *SubEntry` (returns nil on "switch subscription")

- [ ] **Step 1: Update `ShowMenu` signature and implementation**

Replace entire `internal/subscription/menu.go` content:

```go
package subscription

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"xray-runner/internal/ui"
)

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"hysteria2": true,
	"hysteria":  true,
}

func ShowMenu(entries []SubEntry, subURL string) *SubEntry {
	reader := bufio.NewReader(os.Stdin)
	var benchmarkResults []BenchmarkResult

	for {
		ui.Title("Серверы")
		for i, e := range entries {
			hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
			supported := supportedProtocols[e.Protocol]
			remark := e.Remarks
			if remark != "" {
				remark = " [" + remark + "]"
			}
			line := fmt.Sprintf("%-30s  %-5s %s%s", hostPort, e.Protocol, e.Network, remark)
			if benchmarkResults != nil {
				line += "  " + ui.Dim("▸ "+benchmarkResults[i].String())
			}
			tags := []string{}
			if !supported {
				tags = append(tags, "⚠️")
			}
			ui.Item(i+1, line, tags...)
		}
		ui.Divider()

		if benchmarkResults == nil {
			fmt.Printf("  [1-%d] выбор, 'b' бенчмарк, 'r' обновить, 's' сменить подписку: ", len(entries))
		} else {
			fmt.Printf("  Выберите номер (1-%d): ", len(entries))
		}

		input, err := reader.ReadString('\n')
		if err != nil {
			ui.Error("Ошибка ввода")
			continue
		}

		input = strings.TrimSpace(input)

		if strings.EqualFold(input, "b") {
			ui.Progress("Замер latency")
			start := time.Now()
			benchmarkResults = RunBenchmark(entries, 2*time.Second)
			ui.ClearLine()
			ui.Success(fmt.Sprintf("готово (%.1fs)", time.Since(start).Seconds()))
			continue
		}

		if strings.EqualFold(input, "r") {
			ui.Progress("Обновление подписки")
			newEntries, err := Fetch(subURL)
			if err != nil {
				ui.ClearLine()
				ui.Error(fmt.Sprintf("Ошибка: %v", err))
				continue
			}
			entries = newEntries
			benchmarkResults = nil
			ui.ClearLine()
			ui.Success(fmt.Sprintf("Подписка обновлена: %d серверов", len(entries)))
			continue
		}

		if strings.EqualFold(input, "s") {
			return nil
		}

		idx, err := strconv.Atoi(input)
		if err != nil || idx < 1 || idx > len(entries) {
			ui.Error("Некорректный номер. Попробуйте снова.")
			continue
		}

		selected := &entries[idx-1]
		if !supportedProtocols[selected.Protocol] {
			ui.Warn(fmt.Sprintf("Протокол %s не поддерживается. Выберите другой.", selected.Protocol))
			continue
		}

		ui.Success(fmt.Sprintf("Выбран: %s (%s:%d)", selected.Protocol, selected.Address, selected.Port))
		return selected
	}
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build ./internal/subscription/
```
Expected: no errors.

- [ ] **Step 3: Update `internal/app/app.go` to pass subURL to ShowMenu**

Find the line `selected := subscription.ShowMenu(entries)` and change to `selected := subscription.ShowMenu(entries, a.cfg.SubscriptionURL)`.

This change will be fully wired after Task 4 (the subscription selection flow), but the single-line call signature change needs to happen now for compilation.

- [ ] **Step 4: Verify full compilation**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build ./...
```
Expected: no errors.

- [ ] **Step 5: Run tests**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./...
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/subscription/menu.go internal/app/app.go
git commit -m "feat: enhance server menu with ui.* and add r/s controls"
```

---

### Task 4: Modify `internal/app/app.go` with subscription selection flow

**Files:**
- Modify: `internal/app/app.go`

**Interfaces:**
- Consumes: `subscription.LoadSubscriptions()`, `subscription.SaveSubscription()`, `subscription.NamedSubscription`, `subscription.Fetch()`, `ui.*`
- Produces: modified `resolveOutbound` with subscription selection loop + `AddSubFlow` method

- [ ] **Step 1: Add `AddSubFlow` method and update `resolveOutbound` in `internal/app/app.go`**

Add imports in `internal/app/app.go` (add `"net/url"` if not already — it already has `"net/url"`):

Imports already present:
```go
import (
    // existing imports remain
    "xray-runner/internal/subscription"
    // move inside the existing import block, subscription already imported
)
```

Add `"net/url"` import — already present in app.go.

Add `"xray-runner/internal/ui"` to imports.

Replace the `resolveOutbound` method (lines 44-83) with:

```go
func (a *App) resolveOutbound(ctx context.Context) (json.RawMessage, error) {
	// Try loading subscriptions from subscriptions.txt first
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	// No subscriptions file → fallback to .env
	if len(subs) == 0 {
		return a.resolveLegacyOutbound(ctx)
	}

	// Subscription selection loop
	for {
		subURL := a.selectSubscription(subs)
		if subURL == "" {
			// User chose to add new subscription
			if err := a.addSubFlow(); err != nil {
				ui.Error(err.Error())
				// Reload subscriptions
				subs, _ = subscription.LoadSubscriptions()
				if len(subs) == 0 {
					continue
				}
			} else {
				subs, _ = subscription.LoadSubscriptions()
			}
			continue
		}

		// Fetch and select server
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
			return "" // signal "add new"
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

	// Basic URL validation
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		ui.Warn("URL должен начинаться с http:// или https://")
	}

	// Try to validate the URL with a HEAD request
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

Add the missing imports needed (`"strconv"`, `"time"` — check if already imported):

Current imports in app.go:
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

All these imports are already present. Just add `"xray-runner/internal/ui"`.

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

---

### Task 5: Verify end-to-end build and add `subscriptions.txt` to `.gitignore`

- [ ] **Step 1: Build the binary**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build -o xray-runner.exe ./cmd/xray-runner
```
Expected: binary created successfully, no errors.

- [ ] **Step 2: Add `subscriptions.txt` to `.gitignore`**

```bash
echo "subscriptions.txt" >> .gitignore
```

- [ ] **Step 3: Commit**

```bash
git add .gitignore xray-runner.exe
git commit -m "chore: add subscriptions.txt to gitignore, rebuild binary"
```
