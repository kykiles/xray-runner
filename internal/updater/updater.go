// Package updater fetches xray-core releases from the official XTLS GitHub repo
// and installs the core binary or the geo databases in place, replacing whatever
// was there atomically. Rolling back the core is done by re-installing an older
// version from the release list, so no .bak copies are left behind.
package updater

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// CoreRepo is the official xray-core repository; its releases carry the core zip
// per OS/arch. GeoRepo is where the geoip.dat / geosite.dat databases are
// published — the core releases no longer ship them.
const (
	CoreRepo = "XTLS/Xray-core"
	GeoRepo  = "Loyalsoldier/v2ray-rules-dat"
)

const (
	geoipName   = "geoip.dat"
	geositeName = "geosite.dat"
)

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is one published xray-core version with its assets.
type Release struct {
	Tag        string  `json:"tag_name"`
	Name       string  `json:"name"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

func httpClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

// FetchReleases lists the most recent core releases (newest first, up to limit)
// from the official xray-core repo.
func FetchReleases(ctx context.Context, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 20
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", CoreRepo, limit)
	var releases []Release
	if err := getJSON(ctx, url, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// TrimToLatestStable returns the prefix of rels running from the newest release
// down to (and including) the first stable (non-prerelease) one, dropping any
// older pre-release tail. If none of them is stable, rels is returned unchanged
// so the user still has something to pick from.
func TrimToLatestStable(rels []Release) []Release {
	for i, r := range rels {
		if !r.Prerelease {
			return rels[:i+1]
		}
	}
	return rels
}

// FetchLatestGeoRelease returns the newest release of the geo-data repo, which
// carries geoip.dat and geosite.dat.
func FetchLatestGeoRelease(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GeoRepo)
	var r Release
	if err := getJSON(ctx, url, &r); err != nil {
		return Release{}, err
	}
	return r, nil
}

func getJSON(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("запрос к GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API вернул %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("разбор ответа GitHub: %w", err)
	}
	return nil
}

// coreAssetName is the core zip name XTLS publishes for the given OS/arch, e.g.
// "Xray-linux-64.zip". An empty string means the platform is not mapped.
func coreAssetName(goos, goarch string) string {
	var plat string
	switch goos {
	case "linux":
		plat = "linux"
	case "darwin":
		plat = "macos"
	case "windows":
		plat = "windows"
	default:
		return ""
	}
	var arch string
	switch goarch {
	case "amd64":
		arch = "64"
	case "386":
		arch = "32"
	case "arm64":
		arch = "arm64-v8a"
	default:
		return ""
	}
	return fmt.Sprintf("Xray-%s-%s.zip", plat, arch)
}

// CoreAsset returns the core zip matching the current OS/arch, if the release
// carries it.
func CoreAsset(r Release) (Asset, bool) {
	want := coreAssetName(runtime.GOOS, runtime.GOARCH)
	if want == "" {
		return Asset{}, false
	}
	for _, a := range r.Assets {
		if a.Name == want {
			return a, true
		}
	}
	return Asset{}, false
}

// GeoAssets returns the geoip.dat and geosite.dat assets of the release.
func GeoAssets(r Release) (geoip, geosite Asset, ok bool) {
	for _, a := range r.Assets {
		switch a.Name {
		case geoipName:
			geoip = a
		case geositeName:
			geosite = a
		}
	}
	return geoip, geosite, geoip.URL != "" && geosite.URL != ""
}

// download streams the asset to a freshly created temp file and returns its
// path. The caller owns the file and must remove it.
func download(ctx context.Context, a Asset) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("скачивание %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("скачивание %s: %s", a.Name, resp.Status)
	}
	tmp, err := os.CreateTemp("", "xray-update-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("запись %s: %w", a.Name, err)
	}
	tmp.Close()
	return tmp.Name(), nil
}

// InstallCore downloads the core zip, extracts the xray binary and swaps it in
// atomically, then fixes its mode/owner so it stays usable by the invoking user.
func InstallCore(ctx context.Context, a Asset, xrayPath string) error {
	zipPath, err := download(ctx, a)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)

	binName := "xray"
	if runtime.GOOS == "windows" {
		binName = "xray.exe"
	}
	newPath := xrayPath + ".new"
	if err := extractZipFile(zipPath, binName, newPath); err != nil {
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := swap(newPath, xrayPath); err != nil {
		return err
	}
	return finalizeFile(xrayPath, 0o755)
}

// InstallGeo downloads both databases into dir, replacing the existing files.
func InstallGeo(ctx context.Context, geoip, geosite Asset, dir string) error {
	for _, a := range []Asset{geoip, geosite} {
		src, err := download(ctx, a)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, a.Name)
		newPath := dst + ".new"
		if err := os.Rename(src, newPath); err != nil {
			// os.Rename fails across filesystems; fall back to a copy.
			if err := copyFile(src, newPath); err != nil {
				os.Remove(src)
				return err
			}
			os.Remove(src)
		}
		if err := swap(newPath, dst); err != nil {
			return err
		}
		if err := finalizeFile(dst, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// swap atomically replaces dst with newPath. On Linux os.Rename overwrites an
// existing dst in place, so no backup is left behind.
func swap(newPath, dst string) error {
	if err := os.Rename(newPath, dst); err != nil {
		return fmt.Errorf("замена %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// finalizeFile fixes the installed file so it is usable by the person who ran
// the tool: it sets mode, and when running as root under sudo it hands ownership
// back to the invoking user (SUDO_UID/SUDO_GID). Without this a download made
// under sudo lands as root-owned 0600 — unreadable to the user afterwards.
func finalizeFile(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("права %s: %w", filepath.Base(path), err)
	}
	if os.Geteuid() != 0 {
		return nil
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return nil // not launched via sudo; leave ownership as is
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("владелец %s: %w", filepath.Base(path), err)
	}
	return nil
}

// extractZipFile writes the single entry whose base name is want from the zip at
// zipPath to dst.
func extractZipFile(zipPath, want, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("открытие архива: %w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			os.Remove(dst)
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("%s не найден в архиве", want)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
