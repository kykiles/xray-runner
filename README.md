# xray-runner

Go-обёртка над Xray-core для Windows. Читает VLESS/Shadowsocks URL из `.env`, генерирует конфиг, запускает `xray.exe`. Поддерживает два режима работы: HTTP+SOCKS5 прокси и TUN (VPN). При выходе восстанавливает настройки и чистит за собой.

## Быстрый старт

1. Положить `xray.exe`, `geoip.dat`, `geosite.dat`, `wintun.dll` в папку проекта
2. Настроить `.env` (см. ниже)
3. Запустить `xray-runner.exe`

## Параметры `.env`

| Переменная | По умолчанию | Описание |
|---|---|---|
| `VLESS_URL` | — | VLESS или ss:// ссылка (обязательно) |
| `MODE` | `proxy` | Режим: `proxy` (HTTP+SOCKS5) или `tun` (VPN через TUN) |
| `LOG_ENABLED` | `false` | Писать лог в файл |
| `LOG_FILE` | `xray-runner.log` | Путь к файлу лога |
| `LOG_LEVEL` | `info` | Уровень для файлового лога: `debug`, `info`, `warn`, `error` |
| `MASK_CREDENTIALS` | `true` | Маскировать UUID/pbk/sid в выводе |
| `XRAY_LOG_LEVEL` | `warning` | Уровень лога самого Xray: `debug`, `info`, `warning`, `error`, `none` |
| `KILL_SWITCH` | `false` | (TUN) Блокировать трафик при падении Xray через Windows Firewall |

## Структура

```
cmd/xray-runner/main.go   — точка входа (~50 строк)
internal/
  app/       — жизненный цикл: Run(ctx), сборка компонентов
  config/    — .env → Config struct (типизированный)
  log/       — slog-based логгер
  xray/      — запуск/мониторинг/рестарт xray.exe
  xraycfg/   — генерация Xray JSON, типизированные структуры
  system/    — Windows registry proxy, kill switch
template.json              — базовый конфиг Xray (inbounds, DNS, routing)
.env                       — VLESS URL и настройки
xray.exe                   — Xray-core (не компилируется из этого репозитория)
```

## Сборка

```powershell
go build -o xray-runner.exe ./cmd/xray-runner
```

## Режимы работы

### proxy (по умолчанию)
- Xray слушает `127.0.0.1:10808` (SOCKS5) и `127.0.0.1:10809` (HTTP)
- Системный HTTP-прокси Windows включается автоматически
- Весь HTTP-трафик приложений идёт через прокси

### tun
- Xray создаёт виртуальный TUN-адаптер `xray-tun`
- Весь трафик системы идёт через VPN
- DNS обрабатывается Xray
- Опционально: `KILL_SWITCH=true` защищает от утечки IP при обрыве

## Поведение

- Разбирает VLESS (TCP/gRPC/WS + REALITY/TLS/none) или Shadowsocks URL
- При запуске: DNS-резолв → генерация `xray_config.json` → старт `xray.exe` → ожидание портов → тестовый запрос → включение системного прокси/TUN
- Auto-restart: при падении Xray до 5 попыток с exponential backoff
- Health check: каждые 15с проверяет порты, рестарт при 3 последовательных ошибках
- При выходе (Ctrl+C): остановка Xray → удаление `xray_config.json` → восстановление системного прокси/отключение kill switch
- UUID, pbk, sid маскируются в выводе (`49fc...d02`)
