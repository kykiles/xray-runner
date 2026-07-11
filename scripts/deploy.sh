#!/usr/bin/env bash
set -euo pipefail

# === Config ===
XRAY_REPO="github.com/XTLS/Xray-core"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

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

echo "==> Building xray-runner for $GOOS..."
cd "$SCRIPT_DIR"
go build -ldflags "-X main.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o "xray-runner${BIN_EXT}" ./cmd/xray-runner

# === Prepare deploy dir ===
rm -rf "$DEPLOY_DIR"
mkdir -p "$DEPLOY_DIR"

# === Copy runner ===
cp "xray-runner${BIN_EXT}" "$DEPLOY_DIR/"

# === Resolve xray binary ===
if [ -f "$XRAY_BIN" ]; then
    echo "==> Using local $XRAY_BIN"
    cp "$XRAY_BIN" "$DEPLOY_DIR/"
else
    echo "==> $XRAY_BIN not found locally, downloading..."
    XRAY_URL="https://$XRAY_REPO/releases/latest/download/Xray-${GOOS}-64.zip"
    curl -L -o /tmp/xray.zip "$XRAY_URL"
    unzip -o /tmp/xray.zip -d /tmp/xray_extract
    cp "/tmp/xray_extract/$XRAY_BIN" "$DEPLOY_DIR/"
    # Extract assets from zip
    [ -f /tmp/xray_extract/geoip.dat ] && cp /tmp/xray_extract/geoip.dat "$DEPLOY_DIR/"
    [ -f /tmp/xray_extract/geosite.dat ] && cp /tmp/xray_extract/geosite.dat "$DEPLOY_DIR/"
    [ "$GOOS" = "windows" ] && [ -f /tmp/xray_extract/wintun.dll ] && cp /tmp/xray_extract/wintun.dll "$DEPLOY_DIR/"
    rm -rf /tmp/xray_extract /tmp/xray.zip
fi

# === Copy assets (from repo if not already from zip) ===
[ ! -f "$DEPLOY_DIR/geoip.dat" ] && [ -f geoip.dat ] && cp geoip.dat "$DEPLOY_DIR/"
[ ! -f "$DEPLOY_DIR/geosite.dat" ] && [ -f geosite.dat ] && cp geosite.dat "$DEPLOY_DIR/"
[ "$GOOS" = "windows" ] && [ ! -f "$DEPLOY_DIR/wintun.dll" ] && [ -f wintun.dll ] && cp wintun.dll "$DEPLOY_DIR/"
for asset in .env template.json subscriptions.txt; do
    [ -f "$asset" ] && cp "$asset" "$DEPLOY_DIR/"
done

echo "==> Deploy folder: $DEPLOY_DIR"
ls -la "$DEPLOY_DIR/"
