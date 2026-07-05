# xray-runner

Go-обёртка над Xray-core для Windows и Linux. Читает VLESS/Shadowsocks URL или подписку, генерирует конфиг из `template.json`, запускает Xray. Два режима: HTTP+SOCKS5 прокси или TUN (VPN). При выходе чистит за собой.

---

## Сборка

```bash
# Windows
go build -o xray-runner.exe ./cmd/xray-runner

# Linux (кросс-компиляция с Windows)
GOOS=linux GOARCH=amd64 go build -o xray-runner ./cmd/xray-runner

# Через task (Taskfile.yml)
task build                          # под хост
GOOS=linux task build               # кросс-сборка под Linux
```

---

## Состав поставки

### Для пользователя (обе платформы)

```
xray-runner(.exe)   — собранный Go-бинарник
xray(.exe)          — Xray-core (скачать с github.com/XTLS/Xray-core/releases)
template.json       — конфиг-шаблон (inbounds, DNS, routing)
.env                — VLESS_URL или SUBSCRIPTION_URL
geoip.dat           — база GeoIP (из релиза Xray-core)
geosite.dat         — база GeoSite (из релиза Xray-core)
```

Только Windows: `wintun.dll` — если используется TUN-режим.

---

## Настройка `.env`

| Переменная | По умолчанию | Описание |
|---|---|---|
| `VLESS_URL` | — | VLESS или ss:// ссылка (обязательно, если нет `SUBSCRIPTION_URL`) |
| `SUBSCRIPTION_URL` | — | URL подписки (обязательно, если нет `VLESS_URL`). Приоритет выше, чем `VLESS_URL` |
| `MODE` | `proxy` | `proxy` (HTTP+SOCKS5) или `tun` (VPN) |
| `LOG_ENABLED` | `false` | Писать лог в файл |
| `LOG_FILE` | `xray-runner.log` | Путь к лог-файлу |
| `LOG_LEVEL` | `info` | Уровень файлового лога: `debug`, `info`, `warn`, `error` |
| `MASK_CREDENTIALS` | `true` | Маскировать UUID/pbk/sid в выводе |
| `XRAY_LOG_LEVEL` | `warning` | Уровень лога Xray: `debug`, `info`, `warning`, `error`, `none` |
| `KILL_SWITCH` | `false` | Блокировать трафик при падении Xray (TUN mode). Windows: `netsh advfirewall`, Linux: `iptables` |

---

## Режимы

### proxy (по умолчанию)
- SOCKS5: `127.0.0.1:10808`, HTTP: `127.0.0.1:10809`
- **Windows:** системный HTTP-прокси включается через реестр
- **Linux:** системный прокси через GNOME gsettings или KDE kwriteconfig5

### tun
- Виртуальный TUN-адаптер `xray-tun`, весь трафик через VPN
- `KILL_SWITCH=true` — защита от утечки IP при обрыве

---

## Поведение

- Разбирает VLESS (TCP/gRPC/WS + REALITY/TLS/none), Shadowsocks, VMess, Hysteria2
- Подписка: при `SUBSCRIPTION_URL` показывает меню выбора сервера
- DNS-резолв → генерация `xray_config.json` → старт Xray → проверка портов → тестовый запрос → включение системного прокси/TUN
- Auto-restart: до 5 попыток с exponential backoff при падении Xray
- Health check: каждые 15с проверка портов, рестарт при 3 ошибках подряд
- При Ctrl+C: остановка Xray → удаление `xray_config.json` → восстановление прокси/kill switch

---

## Структура проекта

```
cmd/xray-runner/main.go   — точка входа
internal/
  app/       — жизненный цикл Run(ctx)
  config/    — парсинг .env
  log/       — slog-логгер
  xray/      — запуск/мониторинг/рестарт Xray
  xraycfg/   — генерация Xray JSON (VLESS, SS, VMess, Hysteria2)
  system/    — системный прокси и kill switch (Windows: reg+netsh, Linux: gsettings+kde+iptables)
  subscription/ — загрузка и парсинг подписок
template.json  — базовый конфиг Xray
```
