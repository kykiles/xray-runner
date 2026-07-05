# xray-runner

Go-обёртка над Xray-core для Windows. Читает VLESS/Shadowsocks URL из `.env`, генерирует конфиг, запускает `xray.exe`, включает системный HTTP-прокси. При выходе восстанавливает настройки и чистит за собой.

## Быстрый старт

1. Положить `xray.exe`, `geoip.dat`, `geosite.dat` в папку проекта
2. Настроить `.env`:

```env
VLESS_URL=vless://uuid@server:port?type=ws&security=none&path=%2Fvless-ws
LOG_ENABLED=true
LOG_LEVEL=warn
MASK_CREDENTIALS=true
XRAY_LOG_LEVEL=warning
```

3. Запустить `myxray.exe`

## Параметры `.env`

| Переменная | По умолчанию | Описание |
|---|---|---|
| `VLESS_URL` | — | VLESS или ss:// ссылка (обязательно) |
| `LOG_ENABLED` | `false` | Писать лог в файл |
| `LOG_FILE` | `xray-runner.log` | Путь к файлу лога |
| `LOG_LEVEL` | `info` | Уровень для файлового лога: `debug`, `info`, `warn`, `error` |
| `MASK_CREDENTIALS` | `true` | Маскировать UUID/pbk/sid в выводе |
| `XRAY_LOG_LEVEL` | `warning` | Уровень лога самого Xray: `debug`, `info`, `warning`, `error`, `none` |

## Структура

| Файл | Назначение |
|---|---|
| `main.go` | Точка входа, парсинг URL, генерация конфига, запуск Xray, управление прокси |
| `template.json` | Базовый конфиг Xray (inbounds, DNS, routing) |
| `.env` | VLESS URL и настройки |
| `xray.exe` | Xray-core v26.3.27 |
| `myxray.exe` | Скомпилированный `main.go` |

## Сборка

```powershell
$env:CGO_ENABLED=0
go build -o myxray.exe .
```

## Поведение

- Разбирает VLESS (TCP/gRPC/WS + REALITY/TLS/none) или Shadowsocks URL
- При запуске: DNS-резолв сервера → генерация `xray_config.json` → старт `xray.exe` → ожидание портов → тестовый запрос → включение системного прокси
- При выходе (Ctrl+C): остановка Xray → удаление `xray_config.json` → восстановление системного прокси
- При панике или `log.Fatal`: удаление `xray_config.json` (через `defer`)

## Безопасность

- UUID, pbk, sid маскируются в выводе (`49fc...d02`)
- `xray_config.json` удаляется при любом завершении (штатном, ошибке, панике)
- Xray логирует в `warning` по умолчанию (не `debug`)
- Выключить маскировку: `MASK_CREDENTIALS=false`
