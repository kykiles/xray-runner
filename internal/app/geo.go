package app

// The panel's routing names lists that live in the panel's own geo databases —
// geosite:torrent, geosite:twitch-ads, geoip:direct — and xray refuses a config
// naming a list it cannot resolve. The subscription says where those databases
// are (subscription.GeoSources), so install them per subscription and point xray
// at them; what stays unresolved after that is dropped by xraycfg as a last
// resort.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/updater"
	"xray-runner/internal/xraycfg"
)

// panelGeoTTL is how long an installed copy of the panel's databases is reused.
// They are regenerated on the panel's own schedule, and re-downloading tens of
// megabytes on every subscription open would be felt.
const panelGeoTTL = 24 * time.Hour

// useGeoAssets installs the databases the subscription points at (when it points
// at any) and makes both xray and the routing filter read from wherever the
// active ones are. A failure here is never fatal: the core's own databases stay
// in use and the rules naming lists they lack are dropped instead.
func (a *App) useGeoAssets(subURL string, src subscription.GeoSources) {
	dir := filepath.Dir(a.binary)
	defer func() {
		// XRAY_LOCATION_ASSET is inherited by every xray we spawn — the session,
		// the config test and each benchmark probe — so one env var covers them all.
		if err := os.Setenv("XRAY_LOCATION_ASSET", dir); err != nil {
			slog.Warn("не удалось указать xray каталог гео-баз", "dir", dir, "error", err)
		}
		xraycfg.SetGeoAssets(dir)
		slog.Info("geo databases in use", "dir", dir)
	}()

	if src.Empty() {
		return
	}

	panelDir := filepath.Join(filepath.Dir(a.binary), "geo", subKey(subURL))
	if geoFresh(panelDir) {
		dir = panelDir
		return
	}
	if err := os.MkdirAll(panelDir, 0o755); err != nil {
		slog.Warn("гео-базы подписки не установлены", "error", err)
		return
	}
	// Same installer as the update screen: staged download, atomic swap, owner
	// restored after sudo. The panel publishes no checksums, so it warns and
	// installs — a stale or odd geo database cannot execute anything.
	geoip := updater.Asset{Name: "geoip.dat", URL: src.IPURL}
	geosite := updater.Asset{Name: "geosite.dat", URL: src.SiteURL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := updater.InstallGeo(ctx, geoip, geosite, panelDir); err != nil {
		slog.Warn("гео-базы подписки не скачаны, остаюсь на базах ядра", "error", err)
		return
	}
	slog.Info("panel geo databases installed", "dir", panelDir)
	dir = panelDir
}

// geoFresh reports whether both databases are already installed and young enough
// to reuse.
func geoFresh(dir string) bool {
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Size() == 0 || time.Since(fi.ModTime()) > panelGeoTTL {
			return false
		}
	}
	return true
}

// subKey names a subscription's database directory without putting its token in
// a path.
func subKey(subURL string) string {
	sum := sha256.Sum256([]byte(subURL))
	return hex.EncodeToString(sum[:8])
}
