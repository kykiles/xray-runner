# PROJECT.md — xray-runner

Go-обёртка над Xray-core. Читает VLESS/SS/VMess/Hysteria2 URL или подписку, генерирует конфиг из `template.json`, запускает Xray, управляет системным прокси или TUN-интерфейсом. Поддерживает Windows и Linux.

## Project structure

```
cmd/xray-runner/main.go      — точка входа, парсинг флагов
internal/
  app/         — жизненный цикл: resolve outbound → merge config → spawn xray → proxy/tun → health check
  config/      — загрузка .env через godotenv, валидация
  log/         — slog-логгер (консоль + файл)
  xray/        — запуск Xray (FindBinary, Start, Stop, RunWithRetry до 5 попыток с exponential backoff)
  xraycfg/     — типы Xray JSON (VLESS, SS, VMess, Hysteria2), генерация outbound, merge config, TUN inbound, шаблон
  system/      — системный прокси (Windows: реестр, Linux: gsettings/kde) и kill switch (Windows: netsh, Linux: iptables)
  subscription/ — загрузка подписки (HTTP GET → base64 → JSON/URL-list), парсинг, интерактивное меню, бенчмарк
template.json  — базовый конфиг Xray (inbounds, DNS, routing, fallback outbounds)
xray_config.json — генерируется при запуске, удаляется при выходе
.env           — конфиг пользователя (обязателен)
xray(.exe)     — Xray-core (скачивается отдельно с github.com/XTLS/Xray-core/releases)
geoip.dat      — база GeoIP (из релиза Xray-core)
geosite.dat    — база GeoSite (из релиза Xray-core)
wintun.dll     — драйвер TUN для Windows (из релиза Xray-core)
xray_linux/    — собранный дистрибутив для Linux (бинарник xray-runner + xray-core + ассеты)
```

## How it works

1. `main.go` загружает `.env`, парсит `--version` и `--config`
2. `app.Run()` определяет источник конфигурации:
   - `SUBSCRIPTION_URL` → HTTP GET → парсинг подписки → меню выбора сервера (+ опциональный бенчмарк)
   - `VLESS_URL` → прямой парсинг URL
3. DNS-резолв сервера, сборка outbound (VLESS/SS/VMess/Hysteria2)
4. Загрузка `template.json`, замена inbound (proxy → SOCKS5+HTTP, tun → TUN-интерфейс)
5. Merge: prepend proxy outbound + append catch-all routing rule (`tcp,udp` → `proxy`)
6. Запись `xray_config.json`
7. Поиск `xray` (директория бинарника → PATH) → `xray run -c xray_config.json`
8. Авто-рестарт: до 5 попыток с exponential backoff при падении Xray
9. Режим работы:
   - **proxy**: ожидание портов 10808/10809 → тестовый запрос → включение системного прокси → health check каждые 15с
   - **tun**: ожидание интерфейса `xray-tun` → kill switch (опционально) → health check каждые 30с
10. Health check: 3 ошибки подряд → рестарт Xray
11. При Ctrl+C: остановка Xray → восстановление прокси → удаление `xray_config.json`

## Local ports

| Порт | Протокол | Назначение |
|---|---|---|
| 10808 | SOCKS5 | Прокси (входящие соединения) |
| 10809 | HTTP | Прокси (входящие соединения) |

## Configuration (.env)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `VLESS_URL` | — | VLESS/SS/VMess/Hysteria2 ссылка |
| `SUBSCRIPTION_URL` | — | URL подписки |
| `SUBSCRIPTION_REMOTE_URL` | — | Публичная ссылка на Google Doc с URL подписки (скачивается при каждом запуске) |
| `SUBSCRIPTION_SECRET` | — | Пароль для AES-256-GCM шифрования URL в Google Doc. `xray-runner --encrypt "URL"` |
| `MODE` | `proxy` | `proxy` (HTTP+SOCKS5) или `tun` (VPN) |
| `LOG_ENABLED` | `false` | Писать лог в файл |
| `LOG_FILE` | `xray-runner.log` | Путь к лог-файлу |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MASK_CREDENTIALS` | `true` | Маскировать UUID/pbk/sid в выводе |
| `XRAY_LOG_LEVEL` | `warning` | Уровень лога Xray: `debug`, `info`, `warning`, `error`, `none` |
| `KILL_SWITCH` | `false` | Блокировать трафик при падении Xray (TUN mode) |

## Commands

```powershell
# Сборка под Windows
go build -o xray-runner.exe ./cmd/xray-runner

# Кросс-компиляция под Linux
GOOS=linux GOARCH=amd64 go build -o xray-runner ./cmd/xray-runner

# Через Task (Taskfile.yml)
task build                    # под хост
GOOS=linux task build         # кросс-сборка под Linux

# Тесты
go test -v ./...              # все тесты
go test -v -race ./...        # с race detector

# Линтер
golangci-lint run ./...
```

## Supported protocols & transports

| Протокол | Транспорты | Security |
|---|---|---|
| `vless://` | tcp, grpc, ws | reality, tls, none |
| `vmess://` | tcp, ws | tls |
| `ss://` | tcp | — |
| `hysteria2://` | hysteria (UDP) | tls |

Параметры VLESS: `type`, `security`, `flow`, `sni`, `fp`, `pbk`, `sid`, `host`, `path`, `alpn`, `serviceName`.
Параметры Hysteria2: `sni`, `alpn`, `up`/`down` (Mbps), `obfs`, `obfs-password`, `congestion`, `insecure`.

## Platform support

| Функция | Windows | Linux |
|---|---|---|
| Системный прокси | `reg add HKCU\...\Internet Settings` | `gsettings` (GNOME) / `kwriteconfig5` (KDE) |
| Kill switch (TUN) | `netsh advfirewall` | `iptables` |
| TUN-драйвер | `wintun.dll` | встроен в ядро |

## CI/CD

`.github/workflows/ci.yml`: lint (golangci-lint), тесты (Windows + Linux с race detector), сборка. Go 1.24+.
`.github/dependabot.yml`: автообновление Go-модулей и GitHub Actions (ежемесячно).

## Key constraints

- `.env` обязателен: `VLESS_URL` или `SUBSCRIPTION_URL` должны быть заданы
- `xray_config.json` — временный файл, генерируется и удаляется при каждом запуске
- `template.json` — не самостоятельный конфиг, обёртка модифицирует его при запуске
- Xray-core и ассеты (`geoip.dat`, `geosite.dat`, `wintun.dll`) не включены в репозиторий, скачиваются отдельно
- Единственная внешняя Go-зависимость: `github.com/joho/godotenv`
- Сборка — чистый Go, CGO не требуется
