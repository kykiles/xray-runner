// Package updater fetches xray-core releases from the official XTLS GitHub repo
// and installs the core binary or the geo databases in place, replacing whatever
// was there atomically. Rolling back the core is done by re-installing an older
// version from the release list, so no .bak copies are left behind.
package updater

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"xray-runner/internal/system"
)

// maxUncompressedSize caps a single zip entry during extraction so a crafted
// archive can't exhaust the disk (gosec G110). The core binary is ~40 MB; 512 MB
// leaves generous headroom while still bounding the write.
const maxUncompressedSize = 512 << 20

// maxDownloadSize bounds an asset whose size the release JSON did not state;
// when it does, that exact size is the limit instead. maxResponseSize bounds the
// API responses and the checksum files, which are text and tiny by comparison.
const (
	maxDownloadSize = 128 << 20
	maxResponseSize = 8 << 20
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

// downloadClient is for the release artifacts. http.Client.Timeout covers
// reading the body, and the core zip is ~20 MB, so a flat 60s cap would kill
// the download on a slow link. Only the wait for response headers is bounded
// here; the transfer itself is bounded by the caller's context.
func downloadClient() *http.Client {
	return &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}}
}

// SameVersion reports whether a release tag names the installed core version.
// Tags carry a leading "v" (v26.6.27) while the binary reports the bare number
// (26.6.27), so both are normalised before comparing. An empty installed
// version never matches.
func SameVersion(tag, installed string) bool {
	if strings.TrimSpace(installed) == "" {
		return false
	}
	norm := func(s string) string {
		return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "v")
	}
	return norm(tag) == norm(installed)
}

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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(dst); err != nil {
		return fmt.Errorf("разбор ответа GitHub: %w", err)
	}
	return nil
}

// httpGetBytes fetches a small text asset (a checksum file) in full.
func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
}

// fetchChecksum downloads a checksum file next to an asset and extracts the
// expected SHA-256 hex from it using parse.
func fetchChecksum(ctx context.Context, url string, parse func([]byte) (string, error)) (string, error) {
	body, err := httpGetBytes(ctx, url)
	if err != nil {
		return "", err
	}
	return parse(body)
}

// parseDgst reads the SHA-256 hex from an XTLS ".dgst" file, whose lines look
// like "SHA2-256= <hex>".
func parseDgst(body []byte) (string, error) {
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "SHA2-256="); ok {
			if h := strings.TrimSpace(rest); h != "" {
				return h, nil
			}
		}
	}
	return "", fmt.Errorf("в .dgst нет строки SHA2-256")
}

// parseSha256Sum reads the hex from a standard "<hex>  <name>" sha256sum file.
func parseSha256Sum(body []byte) (string, error) {
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("пустой файл контрольной суммы")
	}
	return fields[0], nil
}

// verifySHA256 fails unless the file at path hashes to want.
func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("контрольная сумма %s не совпала: ожидалось %s, получено %s", filepath.Base(path), want, got)
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
	resp, err := downloadClient().Do(req)
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
	// The release JSON states the exact asset size, so anything longer is a
	// server we should not be streaming to disk. Read one byte past the limit to
	// tell a correctly sized asset from an oversized one.
	limit := a.Size
	if limit <= 0 {
		limit = maxDownloadSize
	}
	n, err := io.CopyN(tmp, resp.Body, limit+1)
	if err != nil && err != io.EOF {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("запись %s: %w", a.Name, err)
	}
	if n > limit {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("скачивание %s: размер превышает %d байт", a.Name, limit)
	}
	// Flush to disk before anyone renames this file into place: verifySHA256
	// reads back through the page cache and would not notice a half-written
	// file that a crash later leaves on disk.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("запись %s: %w", a.Name, err)
	}
	_ = tmp.Close()
	return tmp.Name(), nil
}

// InstallCore downloads the core zip, extracts the xray binary and swaps it in
// atomically, then fixes its mode/owner so it stays usable by the invoking user.
func InstallCore(ctx context.Context, a Asset, xrayPath string) error {
	zipPath, err := download(ctx, a)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(zipPath) }()

	// The core binary replaces our own; a corrupted or tampered download must
	// never be installed, so a missing/invalid .dgst is a hard failure here.
	want, err := fetchChecksum(ctx, a.URL+".dgst", parseDgst)
	if err != nil {
		return fmt.Errorf("контрольная сумма ядра недоступна: %w", err)
	}
	if err := verifySHA256(zipPath, want); err != nil {
		return err
	}

	binName := "xray"
	if runtime.GOOS == "windows" {
		binName = "xray.exe"
	}
	newPath := xrayPath + ".new"
	if err := extractZipFile(zipPath, binName, newPath); err != nil {
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		_ = os.Remove(newPath)
		return err
	}
	if err := swap(newPath, xrayPath); err != nil {
		_ = os.Remove(newPath)
		return err
	}
	return finalizeFile(xrayPath, 0o755)
}

// InstallGeo downloads both databases into dir, replacing the existing files.
// Both are fetched and verified before either is swapped in, so a failure on the
// second one leaves the previous pair intact instead of a half-updated mix.
func InstallGeo(ctx context.Context, geoip, geosite Asset, dir string) error {
	staged := make(map[string]string, 2) // dst -> staged .new path
	cleanup := func() {
		for _, newPath := range staged {
			_ = os.Remove(newPath)
		}
	}
	for _, a := range []Asset{geoip, geosite} {
		src, err := download(ctx, a)
		if err != nil {
			cleanup()
			return err
		}
		// The geo repo publishes a "<name>.sha256sum" next to each database.
		// Verify when present; if the checksum can't be fetched, warn and install
		// anyway — a stale geo database is far less dangerous than a swapped core.
		if want, cerr := fetchChecksum(ctx, a.URL+".sha256sum", parseSha256Sum); cerr != nil {
			slog.Warn("контрольная сумма гео-базы недоступна, устанавливаю без проверки", "asset", a.Name, "error", cerr)
		} else if verr := verifySHA256(src, want); verr != nil {
			_ = os.Remove(src)
			cleanup()
			return verr
		}
		dst := filepath.Join(dir, a.Name)
		newPath := dst + ".new"
		if err := os.Rename(src, newPath); err != nil {
			// os.Rename fails across filesystems; fall back to a copy.
			if err := copyFile(src, newPath); err != nil {
				_ = os.Remove(src)
				cleanup()
				return err
			}
			_ = os.Remove(src)
		}
		staged[dst] = newPath
	}
	for dst, newPath := range staged {
		if err := swap(newPath, dst); err != nil {
			cleanup()
			return err
		}
		if err := finalizeFile(dst, 0o644); err != nil {
			cleanup()
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
	if err := system.RestoreSudoOwner(path); err != nil {
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
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		return writeZipEntry(f, dst)
	}
	return fmt.Errorf("%s не найден в архиве", want)
}

// writeZipEntry writes one zip entry to dst. It is its own function so the
// readers close on every path without a defer inside the search loop.
func writeZipEntry(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// Bound the write so a decompression bomb can't fill the disk (gosec
	// G110). io.CopyN stops at the cap and reports io.EOF for a normal,
	// smaller entry; hitting the cap exactly means the entry is oversized.
	n, err := io.CopyN(out, rc, maxUncompressedSize)
	if err != nil && err != io.EOF {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if n == maxUncompressedSize {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("%s в архиве превышает лимит %d байт", filepath.Base(f.Name), maxUncompressedSize)
	}
	// This file is renamed over the running core, so it must be on disk
	// before the swap, not just in the page cache.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
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
		_ = out.Close()
		return err
	}
	return out.Close()
}
