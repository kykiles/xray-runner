# CLI Terminal Enhancement — Design Spec

## Overview

Enhance xray-runner's terminal interface: add subscription management via
`subscriptions.txt`, professional ANSI-styled output, and an interactive
startup flow (select subscription → select server → run Xray).

## Architecture

```
cmd/xray-runner/main.go          — unchanged
internal/
  app/app.go                     — modified resolveOutbound flow
  subscription/
    menu.go                      — enhanced with ui.*, new options
    store.go                     — NEW: load/save subscriptions.txt
  ui/                            — NEW: ANSI styling package
    style.go
    print.go
    input.go
subscriptions.txt                — NEW: one URL per line, # comments
```

## Components

### 1. `internal/ui/` — ANSI Styling (zero dependencies)

- **`style.go`**: ANSI color constants (Cyan, Green, Yellow, Red, Dim, Reset)
  and helper functions `Bold`, `Dim`, `Colored(color, text)`.
- **`print.go`**: Styled output functions:
  - `Title(text)` — `── Подписки ────────────────────────`
  - `Item(i, text, tags...)` — `  1.  Sokol VPN  [sub.sokolvpn.org]`
  - `Success(text)`, `Error(text)`, `Warn(text)` — icon + color + text
  - `Progress(text)` — spinner-style, overwritable by `ClearLine()`
  - `Divider()` — thin rule
- **`input.go`**: `StyledInput(prompt) (string, error)` — colored prompt via
  `bufio.Reader` on `os.Stdin`. Returns trimmed input.

### 2. `internal/subscription/store.go` — Subscription Persistence

- **`LoadSubscriptions() ([]NamedSubscription, error)`**:
  - Reads `subscriptions.txt` from current directory.
  - Skips empty lines and lines starting with `#`.
  - If file doesn't exist, returns empty slice (no error).
  - `NamedSubscription{URL, Name}` — Name extracted from first `#` comment
    preceding the URL, or from hostname if no comment.
- **`SaveSubscription(url string) error`**:
  - Appends URL to `subscriptions.txt`.
  - If file doesn't exist, creates it.
  - Checks for duplicate URL (warns but still saves).
  - Returns error on write failure.
- **`namedFromComment(comment string) string`**: strips `#` prefix, trims.

### 3. `internal/subscription/menu.go` — Enhanced Server Menu

Current menu enhanced with:
- Color via `ui.*` (Cyan titles, Dim hints, Green selected item)
- New controls:
  - `b` — benchmark (existing)
  - `r` — reload subscription (re-fetch URL)
  - `s` — switch subscription (back to subscription selection)
- Prevents leaving menu without a valid selection.
- Error messages in `ui.Error()` red.

### 4. Subscription Selection Menu — Inline in `app.go`

Not a separate file. Logic in `resolveOutbound`:

```
if subscriptions.txt exists:
    1. LoadSubscriptions() → list
    2. Loop:
         a. ui.Title("Подписки")
         b. ui.Item() for each subscription
         c. ui.Item("✚ Добавить новую")
         d. ui.Input(...) for choice
         e. If "add" → AddSubFlow() → append → refresh list
         f. If selection → use URL → break
else:
    if SUBSCRIPTION_URL in .env:
        use it (backward compat)
    else:
        jump to AddSubFlow() — first run

AddSubFlow():
    1. ui.Title("Добавление подписки")
    2. url = ui.StyledInput("Вставьте URL подписки")
    3. name = ui.StyledInput("Название (Enter = из URL)")
    4. optional: best-effort HTTP HEAD to validate URL,
       warn on failure but still save
    5. SaveSubscription(url) (with name as comment)
    6. ui.Success("Подписка добавлена")
```

## Files Modified

| File | Change |
|------|--------|
| `internal/subscription/menu.go` | Replace `fmt.Print` with `ui.*`, add `r`/`s` controls |
| `internal/app/app.go` | `resolveOutbound`: subscription selection flow + AddSubFlow |

## Files Created

| File | Purpose |
|------|---------|
| `internal/ui/style.go` | Colors, Bold, Dim |
| `internal/ui/print.go` | Title, Item, Success, Error, Warn, Progress, Divider |
| `internal/ui/input.go` | StyledInput |
| `internal/subscription/store.go` | LoadSubscriptions, SaveSubscription |
| `subscriptions.txt` | User data, created on first addition |

## Backward Compatibility

- If `subscriptions.txt` does **not** exist, the app falls back to
  `SUBSCRIPTION_URL` or `VLESS_URL` from `.env` (current behavior).
- No changes to `.env` parsing or config structure.
- Existing `template.json`, `xray_config.json` flow untouched.

## Non-Goals

- No interactive server search/filter (YAGNI)
- No encryption of subscriptions file (V2Ray URLs already contain secrets)
- No GUI or full-screen TUI
- No editing or deleting subscriptions via UI (only adding)
