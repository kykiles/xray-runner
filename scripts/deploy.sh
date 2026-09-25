#!/usr/bin/env bash
set -euo pipefail

# === Config ===
XRAY_REPO="github.com/XTLS/Xray-core"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The bundled core is xray.MinVersion: one number for what a bundle ships and
# what TUN and split demand. Not releases/latest — every v26 is a pre-release,
# so latest is stuck on 26.3.27, whose TUN loops the tunnel into itself.
XRAY_VERSION="$(sed -n 's/^const MinVersion = "\(.*\)"$/\1/p' "$SCRIPT_DIR/internal/xray/runner.go")"
[ -n "$XRAY_VERSION" ] || { echo "xray.MinVersion not found in internal/xray/runner.go"; exit 1; }

# === Menu ===
echo "Select target OS:"
echo "1) Linux"
echo "2) Windows"
read -rp "Choice [1-2]: " choice

case "$choice" in
    1) GOOS="linux"; BIN_EXT=""; XRAY_BIN="xray"; DEPLOY_DIR="linux_deploy" ;;
    2) GOOS="windows"; BIN_EXT=".exe"; XRAY_BIN="xray.exe"; DEPLOY_DIR="windows_deploy" ;;
    *) echo "Invalid choice"; exit 1 ;;
esac
export GOOS GOARCH=amd64

echo "==> Building xray-runner for $GOOS/$GOARCH..."
cd "$SCRIPT_DIR"
go build -ldflags "-X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "xray-runner${BIN_EXT}" ./cmd/xray-runner

# === Prepare deploy dir ===
# The core and everything it needs live in bin/, so the folder the user opens
# holds the app, its settings and the subscription list — nothing else. The app
# finds the core there itself (xray.FindBinary), and the geo databases, wintun
# and the updater all follow the core's own directory.
rm -rf "$DEPLOY_DIR"
BIN_DIR="$DEPLOY_DIR/bin"
mkdir -p "$BIN_DIR"

# === Copy runner ===
cp "xray-runner${BIN_EXT}" "$DEPLOY_DIR/"

# === Resolve xray binary ===
# A local core is taken only when it reports the pinned version; any other
# version, or a binary this host cannot run, is replaced by the download.
LOCAL_VERSION="$( ([ -f "$XRAY_BIN" ] && "./$XRAY_BIN" version 2>/dev/null) | awk 'NR==1 {print $2}' || true)"
if [ "$LOCAL_VERSION" = "$XRAY_VERSION" ]; then
    echo "==> Using local $XRAY_BIN $XRAY_VERSION"
    cp "$XRAY_BIN" "$BIN_DIR/"
else
    echo "==> Local $XRAY_BIN is ${LOCAL_VERSION:-absent}, downloading $XRAY_VERSION..."
    TMP_DIR="$(mktemp -d)"
    trap 'rm -rf "$TMP_DIR"' EXIT
    XRAY_ZIP="Xray-${GOOS}-64.zip"
    XRAY_URL="https://$XRAY_REPO/releases/download/v$XRAY_VERSION/$XRAY_ZIP"
    curl -fL -o "$TMP_DIR/$XRAY_ZIP" "$XRAY_URL"
    curl -fL -o "$TMP_DIR/$XRAY_ZIP.dgst" "$XRAY_URL.dgst"
    WANT_SHA="$(sed -n 's/^SHA2-256= *//p' "$TMP_DIR/$XRAY_ZIP.dgst")"
    GOT_SHA="$(sha256sum "$TMP_DIR/$XRAY_ZIP" | cut -d' ' -f1)"
    if [ -z "$WANT_SHA" ] || [ "$WANT_SHA" != "$GOT_SHA" ]; then
        echo "SHA256 mismatch for $XRAY_ZIP: .dgst says '$WANT_SHA', file is '$GOT_SHA'"
        exit 1
    fi
    unzip -o "$TMP_DIR/$XRAY_ZIP" -d "$TMP_DIR/extract"
    cp "$TMP_DIR/extract/$XRAY_BIN" "$BIN_DIR/"
    # Extract assets from zip
    [ -f "$TMP_DIR/extract/geoip.dat" ] && cp "$TMP_DIR/extract/geoip.dat" "$BIN_DIR/"
    [ -f "$TMP_DIR/extract/geosite.dat" ] && cp "$TMP_DIR/extract/geosite.dat" "$BIN_DIR/"
    [ "$GOOS" = "windows" ] && [ -f "$TMP_DIR/extract/wintun.dll" ] && cp "$TMP_DIR/extract/wintun.dll" "$BIN_DIR/"
fi

# === Copy assets (from repo if not already from zip) ===
[ ! -f "$BIN_DIR/geoip.dat" ] && [ -f geoip.dat ] && cp geoip.dat "$BIN_DIR/"
[ ! -f "$BIN_DIR/geosite.dat" ] && [ -f geosite.dat ] && cp geosite.dat "$BIN_DIR/"
[ "$GOOS" = "windows" ] && [ ! -f "$BIN_DIR/wintun.dll" ] && [ -f wintun.dll ] && cp wintun.dll "$BIN_DIR/"
# template.json is embedded in the binary; a copy next to the app is optional.

# The README stays where the user lands; the licenses travel with the binaries
# they cover — wintun.dll may only be redistributed together with its own.
cp README.md "$DEPLOY_DIR/"
cp LICENSE "$BIN_DIR/"
[ "$GOOS" = "windows" ] && cp LICENSE-wintun.txt "$BIN_DIR/"

# Ship the config from the tracked template, never the working .env — the local
# .env and subscriptions hold real tokens and must not leak into a bundle. No
# subscriptions.txt either: one next to the program is the portable, unsealed
# list, and a bundle starts with the sealed one in the data dir.
cp .env.example "$DEPLOY_DIR/.env"

echo "==> Deploy folder: $DEPLOY_DIR"
# TUN without elevation: the service, installed once from the deploy folder.
if [ "$GOOS" = "windows" ]; then
    echo "    TUN without administrator: run once as administrator — .\\xray-runner.exe service install"
else
    echo "    TUN without sudo: run once — sudo ./xray-runner service install"
fi
ls -la "$DEPLOY_DIR/" "$BIN_DIR/"
