# Roadmap: xray-runner → production-grade

## Фаза 1 — Архитектура: рефакторинг в пакеты

### Цель
Разделить monolithic `main.go` (795 строк) на логические пакеты, внедрить `context.Context`, сделать `main.go` точкой входа на ~30 строк.

### Структура директорий

```
internal/
  app/          — жизненный цикл: Run(ctx), сборка компонентов
  config/       — .env → Config struct (типизированный)
  xray/         — запуск/мониторинг/рестарт xray.exe
  xraycfg/      — генерация Xray JSON (buildVless, buildSS, merge)
  system/       — Windows registry proxy management
  log/          — slog-based логгер с level filter
```

### Файлы для реализации

| # | Файл | Описание |
|---|------|----------|
| 1.1 | `internal/config/config.go` | Config struct + `Load()` функция |
| 1.2 | `internal/log/log.go` | slog-based логгер с level filter |
| 1.3 | `internal/xraycfg/types.go` | Типизированные структуры XrayConfig |
| 1.4 | `internal/xraycfg/vless.go` | buildVless (из main.go) |
| 1.5 | `internal/xraycfg/ss.go` | buildSS (из main.go) |
| 1.6 | `internal/xraycfg/builder.go` | readTemplate + mergeConfig |
| 1.7 | `internal/xray/runner.go` | Runner: Start, Stop, Wait, RunWithRetry |
| 1.8 | `internal/system/proxy.go` | enable/restore Windows proxy |
| 1.9 | `internal/system/registry.go` | reg query/add/write helpers |
| 1.10 | `internal/app/app.go` | App: сборка всех компонентов, lifecycle |
| 1.11 | `cmd/xray-runner/main.go` | Точка входа (~30 строк) |
| 1.12 | `go.mod` | Обновить go 1.24 |

### Ключевые изменения

```go
// cmd/xray-runner/main.go
func main() {
    cfg, err := config.Load(".env")
    if err != nil {
        log.Fatalf("config: %v", err)
    }
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
    defer stop()
    if err := app.New(cfg).Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

---

## Фаза 2 — Обработка ошибок и auto-restart

| # | Задача | Суть |
|---|--------|------|
| 2.1 | Context propagation | Все блокирующие операции принимают context.Context |
| 2.2 | Auto-restart | Exponential backoff при падении xray (до 5 попыток) |
| 2.3 | Health check loop | Проверка портов каждые 15с, рестарт при недоступности |
| 2.4 | Rollback прокси | Атомарное включение/восстановление реестра |
| 2.5 | Retry testProxy | 3 попытки с exponential backoff, fallback хосты |

---

## Фаза 3 — Тесты + CI

| # | Пакет | Тесты |
|---|-------|-------|
| 3.1 | `internal/config` | Загрузка .env, default values, missing VLESS_URL |
| 3.2 | `internal/xraycfg` | Table-driven + golden files для всех комбинаций |
| 3.3 | `internal/xray` | Фейковый xray.exe для тестов restart/crash/giveup |
| 3.4 | `internal/system` | Мок `exec.Command("reg", ...)`, тесты парсинга вывода |
| 3.5 | CI | `.github/workflows/ci.yml` — go vet, test, build |
| 3.6 | Dependabot | `.github/dependabot.yml` |

---

## Фаза 4 — Tooling и DX

| # | Инструмент | Назначение |
|---|-----------|-----------|
| 4.1 | `Taskfile.yml` | build, test, lint, clean, run |
| 4.2 | `.golangci.yml` | gofmt, goimports, errcheck, gosec, staticcheck |
| 4.3 | `cmd/main.go` | `--version`, `--help` флаги, ldflags |
| 4.4 | `.gitignore` | Обновить для новых файлов |

---

## Фаза 5 — TUN / VPN режим (опционально)

| # | Задача | Описание |
|---|--------|----------|
| 5.1 | `template.tun.json` | TUN inbound, MTU, IP-стек |
| 5.2 | `--mode tun` флаг | Выбор template.json |
| 5.3 | Kill switch | Firewall правило при падении Xray |
| 5.4 | DNS через TUN | Замена системного DNS, восстановление при выходе |

---

## Порядок выполнения

```
Фаза 1: внутренние пакеты → app → cmd → go build
Фаза 2: контекст → auto-restart → health check → rollback
Фаза 3: юнит-тесты → CI
Фаза 4: Taskfile → linter → флаги
Фаза 5: TUN template → mode selection → kill switch
```
