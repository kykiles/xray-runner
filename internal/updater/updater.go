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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// rename is os.Rename, swapped by tests to fail one step of an install.
var rename = os.Rename

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

// Task #4: walking in and out of the update screen used to re-query the GitHub
// API on every entry and re-download the same geo databases on every →. The
// answers live here for cacheTTL instead, and a geo release installed once in
// this run is not installed again.
const cacheTTL = 10 * time.Minute

var cache struct {
	// ponytail: the mutex is held across the fetch, so two concurrent lookups
	// queue instead of racing. One screen asks at a time; per-slot locking if
	// that ever stops being true.
	mu       sync.Mutex
	releases []Release
	relAt    time.Time
	geo      Release
	geoAt    time.Time
	geoTag   string // geo release already installed in this run
}

// cached returns the memoized value while it is younger than cacheTTL, and
// fetches (and stores) it otherwise. A failed fetch is not cached.
func cached[T any](slot *T, at *time.Time, fetch func() (T, error)) (T, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !at.IsZero() && time.Since(*at) < cacheTTL {
		return *slot, nil
	}
	v, err := fetch()
	if err != nil {
		return v, err
	}
	*slot, *at = v, time.Now()
	return v, nil
}

// GeoInstalled reports whether this geo release was already installed in this
// run — pressing → on "Обновить гео-базы" again would re-download tens of
// megabytes for nothing.
func GeoInstalled(tag string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return tag != "" && cache.geoTag == tag
}

// MarkGeoInstalled records a geo release as installed. Called after the install
// succeeds, so a failed one can be retried.
func MarkGeoInstalled(tag string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.geoTag = tag
}

// FetchReleases lists the most recent core releases (newest first, up to limit)
// from the official xray-core repo. The answer is cached for cacheTTL; the
// cached list ignores limit, which every caller leaves at the same value.
func FetchReleases(ctx context.Context, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 20
	}
	return cached(&cache.releases, &cache.relAt, func() ([]Release, error) {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", CoreRepo, limit)
		var releases []Release
		if err := getJSON(ctx, url, &releases); err != nil {
			return nil, err
		}
		return releases, nil
	})
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
	return cached(&cache.geo, &cache.geoAt, func() (Release, error) {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", GeoRepo)
		var r Release
		if err := getJSON(ctx, url, &r); err != nil {
			return Release{}, err
		}
		return r, nil
	})
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

// verifySHA256 fails unless what r holds hashes to want; name only labels the
// error.
func verifySHA256(r io.Reader, name, want string) error {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("контрольная сумма %s не совпала: ожидалось %s, получено %s", name, want, got)
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

// stage creates the file that will become dir/name: a new temp with a unique
// name in dir itself, so publishing it is a rename within one filesystem, never
// a copy. The O_EXCL create means a link planted under any name in dir is never
// opened, and two installs never share a temp (E02).
func stage(dir, name string) (*os.File, error) {
	f, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("временный файл для %s: %w", name, err)
	}
	return f, nil
}

// discard closes and removes a staged file that is not going to be published.
func discard(f *os.File) {
	_ = f.Close() // already closed once sealed
	_ = os.Remove(f.Name())
}

// download streams the asset into a staged file in dir — the directory it is
// going to be installed in — and returns it open at its start. The caller owns
// the file and must discard it unless it publishes it.
func download(ctx context.Context, a Asset, dir string) (*os.File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачивание %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание %s: %s", a.Name, resp.Status)
	}
	tmp, err := stage(dir, a.Name)
	if err != nil {
		return nil, err
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
		discard(tmp)
		return nil, fmt.Errorf("запись %s: %w", a.Name, err)
	}
	if n > limit {
		discard(tmp)
		return nil, fmt.Errorf("скачивание %s: размер превышает %d байт", a.Name, limit)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		discard(tmp)
		return nil, fmt.Errorf("запись %s: %w", a.Name, err)
	}
	return tmp, nil
}

// InstallCore downloads the core zip, extracts the xray binary into a staged
// file next to xrayPath and renames it over the old core. Only the core's own
// file name is accepted as xrayPath.
func InstallCore(ctx context.Context, a Asset, xrayPath string) error {
	binName := "xray"
	if runtime.GOOS == "windows" {
		binName = "xray.exe"
	}
	if filepath.Base(xrayPath) != binName {
		return fmt.Errorf("ядро ставится только как %s, а не %s", binName, filepath.Base(xrayPath))
	}
	dir := filepath.Dir(xrayPath)
	zf, err := download(ctx, a, dir)
	if err != nil {
		return err
	}
	defer discard(zf)

	// The core binary replaces our own; a corrupted or tampered download must
	// never be installed, so a missing/invalid .dgst is a hard failure here.
	want, err := fetchChecksum(ctx, a.URL+".dgst", parseDgst)
	if err != nil {
		return fmt.Errorf("контрольная сумма ядра недоступна: %w", err)
	}
	if err := verifySHA256(zf, a.Name, want); err != nil {
		return err
	}

	bin, err := stage(dir, binName)
	if err != nil {
		return err
	}
	if err := extractZipFile(zf, binName, bin); err != nil {
		discard(bin)
		return err
	}
	if err := seal(bin, binName, 0o755); err != nil {
		discard(bin)
		return err
	}
	if err := swap(bin.Name(), xrayPath); err != nil {
		discard(bin)
		return err
	}
	return nil
}

// InstallGeo downloads both databases into dir, replacing the existing files.
// Both are fetched, verified and staged before either goes in, and each file
// they replace is moved aside until both are in, so a failure at any point puts
// the previous pair back — the same files, so the same bytes — instead of a
// half-updated mix that dropUnknownGeo then has to paper over. It is a rollback
// on error, not a transaction: a power loss between the two renames still
// leaves a mixed pair.
func InstallGeo(ctx context.Context, geoip, geosite Asset, dir string) error {
	// The installed names are fixed: a name from the release JSON or the
	// subscription never picks the file that gets replaced.
	if geoip.Name != geoipName || geosite.Name != geositeName {
		return fmt.Errorf("гео-базы ставятся только как %s и %s, а не %q и %q", geoipName, geositeName, geoip.Name, geosite.Name)
	}
	assets := []Asset{geoip, geosite}
	staged := make([]*os.File, 0, len(assets))
	defer func() {
		for _, f := range staged {
			if f != nil {
				discard(f)
			}
		}
	}()
	for _, a := range assets {
		f, err := download(ctx, a, dir)
		if err != nil {
			return err
		}
		staged = append(staged, f)
		// The geo repo publishes a "<name>.sha256sum" next to each database.
		// Verify when present; if the checksum can't be fetched, warn and install
		// anyway — a stale geo database is far less dangerous than a swapped core.
		if want, cerr := fetchChecksum(ctx, a.URL+".sha256sum", parseSha256Sum); cerr != nil {
			slog.Warn("контрольная сумма гео-базы недоступна, устанавливаю без проверки", "asset", a.Name, "error", cerr)
		} else if verr := verifySHA256(f, a.Name, want); verr != nil {
			return verr
		}
		if err := seal(f, a.Name, 0o644); err != nil {
			return err
		}
	}

	// The second rename can still fail — a database held open by a running core
	// on Windows — so the first is put back if it does.
	var done []installed
	for i, a := range assets {
		in, err := commit(staged[i].Name(), dir, a.Name)
		if err != nil {
			if rerr := rollback(done); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return err
		}
		staged[i] = nil
		done = append(done, in)
	}
	for _, in := range done {
		if in.backup != "" {
			_ = os.Remove(in.backup)
		}
	}
	return nil
}

// installed is one database InstallGeo has put in: where it went, and where the
// file it replaced was moved aside ("" when there was none).
type installed struct{ dst, backup string }

// commit renames the sealed temp src over dir/name. The file it replaces is
// moved aside first and named in the result, so the caller can put it back; if
// the rename itself fails, commit puts it back on the spot.
func commit(src, dir, name string) (installed, error) {
	dst := filepath.Join(dir, name)
	backup, err := moveAside(dir, name)
	if err != nil {
		return installed{}, err
	}
	if err := swap(src, dst); err != nil {
		if backup != "" {
			if rerr := rename(backup, dst); rerr != nil {
				return installed{}, errors.Join(err, keptAside(name, backup, rerr))
			}
		}
		return installed{}, err
	}
	return installed{dst: dst, backup: backup}, nil
}

// moveAside renames dir/name to a new unique name next to it and returns that
// name, or "" when there was nothing to move. The name is claimed by an O_EXCL
// create and the rename replaces that placeholder, so no fixed name — and no
// link planted under one — takes part.
func moveAside(dir, name string) (string, error) {
	ph, err := os.CreateTemp(dir, "."+name+"-*.old")
	if err != nil {
		return "", fmt.Errorf("резервная копия %s: %w", name, err)
	}
	_ = ph.Close()
	if err := rename(filepath.Join(dir, name), ph.Name()); err != nil {
		_ = os.Remove(ph.Name())
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("резервная копия %s: %w", name, err)
	}
	return ph.Name(), nil
}

// rollback puts back what the databases in done replaced: a moved-aside file
// returns under its name, and a database that did not exist before is removed.
// A file that cannot be put back stays where it was moved, and the error names
// the place.
func rollback(done []installed) error {
	var errs []error
	for _, in := range done {
		if in.backup == "" {
			if err := os.Remove(in.dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("откат %s: %w", filepath.Base(in.dst), err))
			}
			continue
		}
		if err := rename(in.backup, in.dst); err != nil {
			errs = append(errs, keptAside(filepath.Base(in.dst), in.backup, err))
		}
	}
	return errors.Join(errs...)
}

func keptAside(name, backup string, err error) error {
	return fmt.Errorf("прежний %s не возвращён на место, он лежит в %s: %w", name, backup, err)
}

// swap atomically replaces dst with newPath. On Linux os.Rename overwrites an
// existing dst in place, so no backup is left behind.
func swap(newPath, dst string) error {
	if err := rename(newPath, dst); err != nil {
		return fmt.Errorf("замена %s: %w", filepath.Base(dst), err)
	}
	return nil
}

// seal readies a staged file for its rename into place. It sets mode and, when
// running as root under sudo, hands ownership back to the invoking user
// (SUDO_UID/SUDO_GID) — without this a download made under sudo lands as
// root-owned 0600, unreadable to the user afterwards. Both go through the
// descriptor, so no link on disk can redirect them. Then it flushes the file,
// which is about to be renamed over the one in use, and closes it.
func seal(f *os.File, name string, mode os.FileMode) error {
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("права %s: %w", name, err)
	}
	if err := system.RestoreSudoOwnerFile(f); err != nil {
		return fmt.Errorf("владелец %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("запись %s: %w", name, err)
	}
	return f.Close()
}

// extractZipFile writes the single entry of the zip zf whose base name is want
// to dst.
func extractZipFile(zf *os.File, want string, dst io.Writer) error {
	fi, err := zf.Stat()
	if err != nil {
		return fmt.Errorf("открытие архива: %w", err)
	}
	r, err := zip.NewReader(zf, fi.Size())
	if err != nil {
		return fmt.Errorf("открытие архива: %w", err)
	}
	for _, f := range r.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		return writeZipEntry(f, dst)
	}
	return fmt.Errorf("%s не найден в архиве", want)
}

// writeZipEntry writes one zip entry to out. It is its own function so the
// reader closes on every path without a defer inside the search loop.
func writeZipEntry(f *zip.File, out io.Writer) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	// Bound the write so a decompression bomb can't fill the disk (gosec
	// G110). io.CopyN stops at the cap and reports io.EOF for a normal,
	// smaller entry; hitting the cap exactly means the entry is oversized.
	n, err := io.CopyN(out, rc, maxUncompressedSize)
	if err != nil && err != io.EOF {
		return err
	}
	if n == maxUncompressedSize {
		return fmt.Errorf("%s в архиве превышает лимит %d байт", filepath.Base(f.Name), maxUncompressedSize)
	}
	return nil
}
