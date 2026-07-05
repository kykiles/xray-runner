# AGENTS.md — xray-runner

Thin Go wrapper that reads a proxy URL (`vless://` or `ss://`), generates an Xray config from a template, and spawns the real `xray.exe` binary.

## Project structure

```
main.go          — entrypoint: parse VLESS URL → generate config → launch xray.exe
template.json    — base Xray config (DNS, inbounds, routing rules, fallback outbounds)
xray_config.json — auto-generated at runtime by main.go, deleted on graceful shutdown
.env             — must contain `VLESS_URL=<vless://...>` (loaded via godotenv)
xray.exe         — Xray-core binary (not compiled from this repo)
geoip.dat        — geoip routing data (Xray asset)
geosite.dat      — geosite routing data (Xray asset)
wintun.dll       — Windows TUN driver (Xray dependency)
```

## How it works

1. `main.go` loads `.env`, parses the proxy URL (supports `vless://` and `ss://`)
2. Reads `template.json` as base config
3. Builds a dynamic outbound from the URL params (VLESS or Shadowsocks)
4. Prepends it to `template.json`'s outbounds (direct, block) and appends a catch-all routing rule (`network: tcp,udp → proxy`)
5. Writes the merged config to `xray_config.json`
6. Spawns `xray.exe run -c xray_config.json` (searches exe dir first, then CWD)
7. Enables system HTTP proxy (`127.0.0.1:10809`) in Windows registry
8. On Ctrl+C: stops xray, restores original proxy settings, deletes `xray_config.json`

## Local proxy ports

- SOCKS5: `127.0.0.1:10808`
- HTTP: `127.0.0.1:10809`
(defined in `template.json` `inbounds`)

## Commands

```powershell
# Build the Go wrapper
go build -o xray-runner.exe .

# Run (must have .env, xray.exe, geoip.dat, geosite.dat, wintun.dll, template.json in CWD)
go run .

# Build the Go wrapper then run
go build -o xray-runner.exe .; if ($?) { .\xray-runner.exe }
```

## Supported protocols & transports

| Протокол | Параметры URL |
|---|---|
| `vless://` | `type` (tcp/grpc/ws), `security` (reality/tls), `flow`, `sni`, `fp`, `pbk`, `sid`, `host`, `path`, `alpn` |
| `ss://` | base64(`method:password`)@host:port |

Транспорты VLESS: `tcp`, `grpc`, `ws` (WebSocket с `wsSettings`). Security: `reality`, `tls` (с `tlsSettings`, `serverName`, `fingerprint`, `alpn`).

## Key constraints

- **Go build always manual** — `go build -o xray-runner.exe .` is run by hand, never by scripts or the wrapper itself
- `.env` **must** exist with `VLESS_URL` set to a valid `vless://...` or `ss://...` URI
- `ss://` links use standard base64(`method:password`) format (with auto-padding)
- `template.json` is **not** a standalone Xray config — the Go wrapper modifies it at runtime (adds proxy outbound, appends catch-all rule)
- `xray_config.json` is ephemeral; never edit it directly (it is overwritten each run)
- `xray.exe` + `geoip.dat` + `geosite.dat` + `wintun.dll` must be discoverable (same dir as the wrapper or CWD)
- Only dependency: `github.com/joho/godotenv` (for `.env` loading)

## Legacy scripts (not used by Go wrapper)

- `xray_no_window.ps1` — `Start-Process xray.exe -WindowStyle Hidden`
- `xray_no_window.vbs` — `Run "xray.exe -config config.json", 0`
Both run `xray.exe` directly with a static config; they predate the Go wrapper.

## What is NOT in this repo

- No tests, no CI, no linter config, no typecheck script
- No pre-commit hooks or task runners
- The README is the upstream Xray-core README; the one-line build command there builds Xray-core itself, not this wrapper
