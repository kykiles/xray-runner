package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	path, err := download(ctx, Asset{Name: "stalled", URL: srv.URL})
	if err == nil {
		_ = os.Remove(path)
		t.Fatal("download of a stalled body returned no error")
	}
}

// A server that sends more than the release JSON promised must not be streamed
// to disk in full.
func TestDownloadRejectsOversizedBody(t *testing.T) {
	body := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	path, err := download(context.Background(), Asset{Name: "fat", URL: srv.URL, Size: 100})
	if err == nil {
		_ = os.Remove(path)
		t.Fatal("download accepted a body larger than Asset.Size")
	}

	// The exact declared size still goes through.
	path, err = download(context.Background(), Asset{Name: "exact", URL: srv.URL, Size: int64(len(body))})
	if err != nil {
		t.Fatalf("download of an exactly sized asset: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	got, err := os.Stat(path)
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
		if _, err := os.Stat(filepath.Join(dir, name+".new")); !os.IsNotExist(err) {
			t.Errorf("%s.new left behind: stat err = %v", name, err)
		}
	}
}

func TestInstallCoreCleansUpNewOnSwapFailure(t *testing.T) {
	dir := t.TempDir()
	binName := "xray"
	if runtime.GOOS == "windows" {
		binName = "xray.exe"
	}
	zipPath := filepath.Join(dir, "core.zip")
	writeZip(t, zipPath, map[string]string{binName: "BINARY"})
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
	defer srv.Close()

	// A directory can't be replaced by renaming a file over it, so the swap
	// fails after the .new file is already on disk.
	xrayPath := filepath.Join(dir, "xray-target")
	if err := os.Mkdir(xrayPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err = InstallCore(context.Background(), Asset{Name: "Xray.zip", URL: srv.URL + "/Xray.zip"}, xrayPath)
	if err == nil {
		t.Fatal("InstallCore over a directory should fail")
	}
	if _, err := os.Stat(xrayPath + ".new"); !os.IsNotExist(err) {
		t.Errorf("failed swap left %s.new behind: stat err = %v", xrayPath, err)
	}
}
