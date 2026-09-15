package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The core zip is tens of megabytes, so a flat client Timeout would abort a
// slow but healthy download. Only the header wait may be bounded.
func TestDownloadClientHasNoBodyTimeout(t *testing.T) {
	c := downloadClient()
	if c.Timeout != 0 {
		t.Errorf("downloadClient().Timeout = %v, want 0", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want a positive bound", tr.ResponseHeaderTimeout)
	}
}

// Losing the client Timeout must not cost us the ability to abort: the context
// still has to cut a download that stalls mid-body.
func TestDownloadStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	dir := t.TempDir()
	f, err := download(ctx, Asset{Name: "stalled", URL: srv.URL}, dir)
	if err == nil {
		discard(f)
		t.Fatal("download of a stalled body returned no error")
	}
	assertDirHolds(t, dir)
}

// A server that sends more than the release JSON promised must not be streamed
// to disk in full.
func TestDownloadRejectsOversizedBody(t *testing.T) {
	body := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f, err := download(context.Background(), Asset{Name: "fat", URL: srv.URL, Size: 100}, dir)
	if err == nil {
		discard(f)
		t.Fatal("download accepted a body larger than Asset.Size")
	}
	assertDirHolds(t, dir)

	// The exact declared size still goes through.
	f, err = download(context.Background(), Asset{Name: "exact", URL: srv.URL, Size: int64(len(body))}, dir)
	if err != nil {
		t.Fatalf("download of an exactly sized asset: %v", err)
	}
	defer discard(f)
	got, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if got.Size() != int64(len(body)) {
		t.Errorf("downloaded %d bytes, want %d", got.Size(), len(body))
	}
}

// A bad second database must leave the first one at its previous version rather
// than half-updating the pair.
func TestInstallGeoKeepsOldPairWhenSecondFails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{geoipName, geositeName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("OLD "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + geoipName, "/" + geositeName:
			_, _ = w.Write([]byte("NEW"))
		case "/" + geoipName + ".sha256sum":
			sum := sha256.Sum256([]byte("NEW"))
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + geoipName + "\n"))
		case "/" + geositeName + ".sha256sum":
			// Checksum of something else: the second database fails to verify.
			sum := sha256.Sum256([]byte("OTHER"))
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + geositeName + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	geoip := Asset{Name: geoipName, URL: srv.URL + "/" + geoipName}
	geosite := Asset{Name: geositeName, URL: srv.URL + "/" + geositeName}
	if err := InstallGeo(context.Background(), geoip, geosite, dir); err == nil {
		t.Fatal("InstallGeo with a mismatched checksum returned no error")
	}

	for _, name := range []string{geoipName, geositeName} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "OLD "+name {
			t.Errorf("%s = %q, want the old copy untouched", name, got)
		}
	}
	assertDirHolds(t, dir, geoipName, geositeName)
}

func TestInstallCoreCleansUpOnSwapFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory can't be replaced by renaming a file over it, so the swap
	// fails after the staged binary is already on disk.
	xrayPath := filepath.Join(dir, coreBin())
	if err := os.Mkdir(xrayPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallCore(context.Background(), coreServer(t, "BINARY"), xrayPath); err == nil {
		t.Fatal("InstallCore over a directory should fail")
	}
	assertDirHolds(t, dir, coreBin())
}

func TestInstallCore_InstallsExecutableWithoutDebris(t *testing.T) {
	dir := t.TempDir()
	xrayPath := filepath.Join(dir, coreBin())
	if err := os.WriteFile(xrayPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallCore(context.Background(), coreServer(t, "BINARY"), xrayPath); err != nil {
		t.Fatalf("InstallCore: %v", err)
	}

	if got := readFile(t, xrayPath); got != "BINARY" {
		t.Errorf("core = %q, want BINARY", got)
	}
	// Windows keeps no exec bit: Chmod there only toggles read-only.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(xrayPath)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("core mode = %o, want 755", fi.Mode().Perm())
		}
	}
	assertDirHolds(t, dir, coreBin())
}

// Only the core's own file is ever replaced, whatever path the caller passes.
func TestInstallCore_RefusesAForeignName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sh")
	if err := InstallCore(context.Background(), refusingServer(t), target); err == nil {
		t.Fatal("InstallCore replaced a file that is not the core")
	}
	assertDirHolds(t, dir)
}

// Only geoip.dat and geosite.dat are installed: a name from the release JSON or
// the subscription never picks the file that gets replaced.
func TestInstallGeo_RefusesForeignNames(t *testing.T) {
	cases := map[string][2]string{
		"escaping":  {"../" + geoipName, geositeName},
		"other":     {geoipName, "hosts"},
		"swapped":   {geositeName, geoipName},
		"duplicate": {geoipName, geoipName},
	}
	for name, names := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "geo")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			a := refusingServer(t)
			geoip := Asset{Name: names[0], URL: a.URL}
			geosite := Asset{Name: names[1], URL: a.URL}
			if err := InstallGeo(context.Background(), geoip, geosite, dir); err == nil {
				t.Fatalf("InstallGeo accepted the names %q", names)
			}
			assertDirHolds(t, dir)
			assertDirHolds(t, root, "geo")
		})
	}
}

func TestInstallGeo_SecondDownloadFailsKeepsOldPair(t *testing.T) {
	dir := t.TempDir()
	writeOldGeo(t, dir)
	geoip, geosite := geoServer(t, newGeo, nil)
	geosite.URL += ".missing"

	if err := InstallGeo(context.Background(), geoip, geosite, dir); err == nil {
		t.Fatal("InstallGeo with a failed second download returned no error")
	}
	assertOldGeo(t, dir)
	assertDirHolds(t, dir, geoipName, geositeName)
}

// The second database failing to go in puts the first one back as it was.
func TestInstallGeo_SecondCommitFailsRestoresFirst(t *testing.T) {
	dir := t.TempDir()
	writeOldGeo(t, dir)
	geoip, geosite := geoServer(t, newGeo, nil)
	sitePath := filepath.Join(dir, geositeName)
	failRenames(t, func(from, to string) bool {
		return to == sitePath && !strings.HasSuffix(from, ".old") // the publish, not a restore
	})

	if err := InstallGeo(context.Background(), geoip, geosite, dir); err == nil {
		t.Fatal("InstallGeo with a failed second commit returned no error")
	}
	assertOldGeo(t, dir)
	assertDirHolds(t, dir, geoipName, geositeName)
}

// A rollback that fails is reported, and the copy it could not put back stays
// on disk under the name the error gives.
func TestInstallGeo_FailedRollbackKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	writeOldGeo(t, dir)
	geoip, geosite := geoServer(t, newGeo, nil)
	ipPath, sitePath := filepath.Join(dir, geoipName), filepath.Join(dir, geositeName)
	failRenames(t, func(from, to string) bool {
		restore := strings.HasSuffix(from, ".old")
		return (to == sitePath && !restore) || (to == ipPath && restore)
	})

	err := InstallGeo(context.Background(), geoip, geosite, dir)
	if err == nil {
		t.Fatal("InstallGeo with a failed commit and rollback returned no error")
	}
	if got := readFile(t, ipPath); got != newGeo(geoipName) {
		t.Fatalf("%s = %q, want the new copy the failed rollback left", geoipName, got)
	}
	if got := readFile(t, sitePath); got != oldGeo(geositeName) {
		t.Errorf("%s = %q, want the old copy back", geositeName, got)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var backup string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.Type().IsRegular() && readFile(t, p) == oldGeo(geoipName) {
			backup = p
		}
	}
	if backup == "" {
		t.Fatalf("the old %s is gone: no file holds its bytes", geoipName)
	}
	if !strings.Contains(err.Error(), backup) {
		t.Errorf("error %q does not name the backup %s", err, backup)
	}
}

// Two installs into one directory each keep their own staged files: the one
// that finishes first must not take or delete what the other has staged.
func TestInstallGeo_ConcurrentInstallsKeepTheirStaging(t *testing.T) {
	dir := t.TempDir()
	writeOldGeo(t, dir)

	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	slowBody := func(name string) string { return "SLOW " + name }
	slowIP, slowSite := geoServer(t, slowBody, func(name string) {
		if name == geositeName {
			close(arrived)
			<-release
		}
	})
	fastIP, fastSite := geoServer(t, newGeo, nil)

	slowErr := make(chan error, 1)
	go func() { slowErr <- InstallGeo(context.Background(), slowIP, slowSite, dir) }()
	<-arrived // the slow install has geoip staged and is fetching geosite

	if err := InstallGeo(context.Background(), fastIP, fastSite, dir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	unblock()
	if err := <-slowErr; err != nil {
		t.Fatalf("first install lost what it had staged: %v", err)
	}
	for _, db := range []string{geoipName, geositeName} {
		if got := readFile(t, filepath.Join(dir, db)); got != slowBody(db) {
			t.Errorf("%s = %q, want %q from the install that finished last", db, got, slowBody(db))
		}
	}
	assertDirHolds(t, dir, geoipName, geositeName)
}

func coreBin() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
}

// coreServer serves a core zip holding bin as the core binary, with its .dgst.
func coreServer(t *testing.T, bin string) Asset {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "core.zip")
	writeZip(t, zipPath, map[string]string{coreBin(): bin})
	zipBody, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBody)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Ext(r.URL.Path) == ".dgst" {
			_, _ = w.Write([]byte("SHA2-256= " + hex.EncodeToString(sum[:]) + "\n"))
			return
		}
		_, _ = w.Write(zipBody)
	}))
	t.Cleanup(srv.Close)
	return Asset{Name: "Xray.zip", URL: srv.URL + "/Xray.zip"}
}

// refusingServer fails the test on any request: the install had to be refused
// before anything was downloaded.
func refusingServer(t *testing.T) Asset {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return Asset{Name: "Xray.zip", URL: srv.URL + "/Xray.zip"}
}

// geoServer serves both databases, body giving each one's content, with their
// .sha256sum files. onBody, when set, runs before a database is sent.
func geoServer(t *testing.T, body func(name string) string, onBody func(name string)) (geoip, geosite Asset) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, isSum := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".sha256sum")
		if name != geoipName && name != geositeName {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if isSum {
			sum := sha256.Sum256([]byte(body(name)))
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
			return
		}
		if onBody != nil {
			onBody(name)
		}
		_, _ = w.Write([]byte(body(name)))
	}))
	t.Cleanup(srv.Close)
	return Asset{Name: geoipName, URL: srv.URL + "/" + geoipName},
		Asset{Name: geositeName, URL: srv.URL + "/" + geositeName}
}

func newGeo(name string) string { return "NEW " + name }

// oldGeo is not text on purpose: a rollback must bring back the exact bytes.
func oldGeo(name string) string { return "OLD\x00\xff\r\n" + name }

func writeOldGeo(t *testing.T, dir string) {
	t.Helper()
	for _, db := range []string{geoipName, geositeName} {
		if err := os.WriteFile(filepath.Join(dir, db), []byte(oldGeo(db)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertOldGeo(t *testing.T, dir string) {
	t.Helper()
	for _, db := range []string{geoipName, geositeName} {
		if got := readFile(t, filepath.Join(dir, db)); got != oldGeo(db) {
			t.Errorf("%s = %q, want the old copy byte for byte", db, got)
		}
	}
}

// failRenames makes every rename that fail picks fail, as a file held open by a
// running core does on Windows.
func failRenames(t *testing.T, fail func(from, to string) bool) {
	t.Helper()
	orig := rename
	rename = func(from, to string) error {
		if fail(from, to) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("файл занят")}
		}
		return orig(from, to)
	}
	t.Cleanup(func() { rename = orig })
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertDirHolds fails unless dir holds exactly the named entries: no staged
// temp and no moved-aside copy left behind.
func assertDirHolds(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s holds %q, want %q", filepath.Base(dir), got, want)
	}
}
