package app

// The panel's routing names lists that live in the panel's own geo databases —
// geosite:torrent, geosite:twitch-ads, geoip:direct — and xray refuses a config
// naming a list it cannot resolve. The subscription says where those databases
// are (subscription.GeoSources), so install them per subscription and point xray
// at them; a list still missing after that stops the config with its name
// (xraycfg.CheckGeoLists) rather than the rule naming it being dropped.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/updater"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// panelGeoTTL is how long an installed copy of the panel's databases is reused.
// They are regenerated on the panel's own schedule, and re-downloading tens of
// megabytes on every subscription open would be felt.
const panelGeoTTL = 24 * time.Hour

// useGeoAssets installs the databases the subscription points at (when it points
// at any) and makes both xray and the geo check read from wherever the active
// ones are. A failed update stops nothing by itself: the panel's previous pair
// stays in use — the core's own databases when there is none — the session's
// screen says so (noteGeoDatabases), and a config naming lists they lack is
// refused when it is built, the failed update named (xraycfg.CheckGeoLists).
func (a *App) useGeoAssets(subURL string, src subscription.PanelInfo) {
	dir, note := filepath.Dir(a.binary), ""
	defer func() {
		// XRAY_LOCATION_ASSET is inherited by every xray we spawn — the session,
		// the config test and each benchmark probe — so one env var covers them all.
		if err := os.Setenv("XRAY_LOCATION_ASSET", dir); err != nil {
			slog.Warn("не удалось указать xray каталог гео-баз", "dir", dir, "error", err)
		}
		xraycfg.SetGeoAssets(dir, note)
		a.geoNote = note
		slog.Info("geo databases in use", "dir", dir)
	}()

	a.dropLegacyGeoDir()
	if src.GeoEmpty() {
		return
	}

	// In the user's cache, not beside the core (H03): a core found on PATH sits
	// among somebody else's files. Made the way the data dir is, so a run
	// without sudo can read and replace what a sudo run downloaded.
	root, err := config.CacheSubdir("geo")
	if err != nil {
		slog.Warn("гео-базы подписки не установлены", "error", err)
		note = fmt.Sprintf("Гео-базы панели не скачаны (%v) — используются базы ядра.", err)
		return
	}
	pruneGeoDirs(root, subURL)

	panelDir := filepath.Join(root, subKey(subURL))
	if age, ok := geoAge(panelDir); ok && age <= panelGeoTTL {
		dir = panelDir
		return
	}
	if panelDir, err = config.CacheSubdir("geo", subKey(subURL)); err != nil {
		slog.Warn("гео-базы подписки не установлены", "error", err)
		note = fmt.Sprintf("Гео-базы панели не скачаны (%v) — используются базы ядра.", err)
		return
	}
	// Same staged install as the update screen, held to the panel's checksum
	// policy: https only, and a checksum enforced when the panel publishes one.
	geoip := updater.Asset{Name: "geoip.dat", URL: src.IPURL}
	geosite := updater.Asset{Name: "geosite.dat", URL: src.SiteURL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := updater.InstallPanelGeo(ctx, geoip, geosite, panelDir); err != nil {
		// E03: a failed install puts the previous pair back as it was, age
		// included, so the next open tries again. The panel's rules were written
		// against that pair, so it serves until then; whether it still carries
		// every list they name is the geo check's call.
		if _, ok := geoAge(panelDir); ok {
			slog.Warn("гео-базы подписки не обновились, работаю на прошлой копии", "error", err)
			note = fmt.Sprintf("Гео-базы панели не обновились (%v) — используется прошлая скачанная копия.", err)
			dir = panelDir
			return
		}
		slog.Warn("гео-базы подписки не скачаны, остаюсь на базах ядра", "error", err)
		note = fmt.Sprintf("Гео-базы панели не скачаны (%v) — используются базы ядра.", err)
		return
	}
	slog.Info("panel geo databases installed", "dir", panelDir)
	dir = panelDir
}

// dropLegacyGeoDir removes the geo/ directory earlier versions kept the panel's
// databases in, beside the core. Only beside the program's own core: that
// directory is the program's; beside a core found on PATH nothing is touched.
// What was there is downloaded again into the cache on the next open.
func (a *App) dropLegacyGeoDir() {
	if !xray.Bundled(a.binary, config.ProgramDir()) {
		return
	}
	old := filepath.Join(filepath.Dir(a.binary), "geo")
	if _, err := os.Lstat(old); err != nil {
		return
	}
	if err := os.RemoveAll(old); err != nil {
		slog.Warn("старые гео-базы рядом с ядром не удалены", "dir", old, "error", err)
		return
	}
	slog.Info("old geo databases beside the core removed, the cache holds them now", "dir", old)
}

// noteGeoDatabases puts the note of a failed panel geo update on the screen the
// session opens with: its rules are checked against databases other than the
// panel's current ones. Called at bring-up, before any screen is up.
func (a *App) noteGeoDatabases() {
	if a.geoNote == "" {
		return
	}
	note := a.geoNote
	if a.pendingNote != "" {
		note = a.pendingNote + " " + note
	}
	a.pendingNote = note
}

// geoAge reports whether both databases are installed in dir, and how old the
// older of the two is.
func geoAge(dir string) (time.Duration, bool) {
	var age time.Duration
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Size() == 0 {
			return 0, false
		}
		age = max(age, time.Since(fi.ModTime()))
	}
	return age, true
}

// pruneGeoDirs removes the database directories of subscriptions that are gone.
// Deleting one costs nothing — it is re-downloaded on the next open — so the
// saved list plus the subscription in hand (which may come from the env and
// never reach that list) is the whole of what is worth keeping.
func pruneGeoDirs(root, keepURL string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return // nothing installed yet
	}
	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		slog.Warn("список подписок не прочитан, старые гео-базы оставлены", "error", err)
		return
	}

	keep := map[string]bool{subKey(keepURL): true}
	for _, s := range subs {
		keep[subKey(s.URL)] = true
	}
	for _, e := range entries {
		if !e.IsDir() || keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			slog.Warn("старые гео-базы не удалены", "dir", e.Name(), "error", err)
			continue
		}
		slog.Info("stale geo databases removed", "dir", e.Name())
	}
}

// subKey names a subscription's database directory without putting its token in
// a path.
func subKey(subURL string) string {
	sum := sha256.Sum256([]byte(subURL))
	return hex.EncodeToString(sum[:8])
}
