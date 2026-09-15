package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// secretToken stands for a token in a panel's database URL: no error may show it.
const secretToken = "SECRETTOKEN"

// The geo release publishes a .sha256sum next to each database, so an install
// from it that cannot check one is refused and the previous pair stays (E02).
func TestInstallReleaseGeo_RequiresTheChecksum(t *testing.T) {
	for mode, wantOK := range map[string]bool{
		"ok":           true,
		"missing":      false,
		"server error": false,
		"empty":        false,
		"malformed":    false,
		"short":        false,
		"mismatch":     false,
	} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeOldGeo(t, dir)
			geoip, geosite := sumServer(t, httptest.NewServer, mode)
			err := InstallReleaseGeo(context.Background(), geoip, geosite, dir)
			assertGeoOutcome(t, dir, err, wantOK)
		})
	}
}

// A panel often publishes no checksum, so one it answers 404 for is taken as
// none and the pair goes in on HTTPS alone. A checksum it does serve — and any
// other failure to fetch one — is enforced like the release's (E02).
func TestInstallPanelGeo_EnforcesAPublishedChecksum(t *testing.T) {
	for mode, wantOK := range map[string]bool{
		"ok":           true,
		"missing":      true,
		"server error": false,
		"empty":        false,
		"malformed":    false,
		"short":        false,
		"mismatch":     false,
	} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeOldGeo(t, dir)
			geoip, geosite := sumServer(t, httptest.NewTLSServer, mode)
			err := InstallPanelGeo(context.Background(), geoip, geosite, dir)
			assertGeoOutcome(t, dir, err, wantOK)
		})
	}
}

// The panel's databases come over HTTPS only: loopback http, which the transport
// rule lets tests use, is refused before any request.
func TestInstallPanelGeo_RequiresHTTPS(t *testing.T) {
	dir := t.TempDir()
	a := refusingServer(t)
	geoip := Asset{Name: geoipName, URL: a.URL}
	geosite := Asset{Name: geositeName, URL: a.URL}
	if err := InstallPanelGeo(context.Background(), geoip, geosite, dir); err == nil {
		t.Fatal("InstallPanelGeo accepted plain http")
	}
	assertDirHolds(t, dir)
}

// net/http puts the whole URL in its error text, and a panel's database URL may
// carry a token: an updater error names the host at most.
func TestInstallPanelGeo_ErrorsHideThePath(t *testing.T) {
	t.Run("server gone", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.NotFoundHandler())
		trustTLS(t, srv)
		srv.Close()
		base := srv.URL + "/" + secretToken + "/"
		geoip := Asset{Name: geoipName, URL: base + geoipName}
		geosite := Asset{Name: geositeName, URL: base + geositeName}
		assertHidesToken(t, InstallPanelGeo(context.Background(), geoip, geosite, t.TempDir()))
	})
	t.Run("checksum redirected to http", func(t *testing.T) {
		geoip, geosite := sumServer(t, httptest.NewTLSServer, "to http")
		assertHidesToken(t, InstallPanelGeo(context.Background(), geoip, geosite, t.TempDir()))
	})
}

// sumServer serves the new geo pair under a path holding secretToken, from
// newServer — httptest.NewServer or NewTLSServer. Each .sha256sum answers as
// mode says; "ok" is the right one.
func sumServer(t *testing.T, newServer func(http.Handler) *httptest.Server, mode string) (geoip, geosite Asset) {
	t.Helper()
	srv := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, isSum := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"+secretToken+"/"), ".sha256sum")
		if name != geoipName && name != geositeName {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !isSum {
			_, _ = w.Write([]byte(newGeo(name)))
			return
		}
		sum := sha256.Sum256([]byte(newGeo(name)))
		good := hex.EncodeToString(sum[:])
		other := sha256.Sum256([]byte("OTHER"))
		switch mode {
		case "ok":
			_, _ = w.Write([]byte(good + "  " + name + "\n"))
		case "missing":
			w.WriteHeader(http.StatusNotFound)
		case "server error":
			w.WriteHeader(http.StatusInternalServerError)
		case "empty":
			// 200 with no body
		case "malformed":
			_, _ = w.Write([]byte("<!doctype html>  " + name + "\n"))
		case "short":
			_, _ = w.Write([]byte(good[:16] + "  " + name + "\n"))
		case "mismatch":
			_, _ = w.Write([]byte(hex.EncodeToString(other[:]) + "  " + name + "\n"))
		case "to http":
			http.Redirect(w, r, "http://127.0.0.1/"+secretToken+"/sum", http.StatusFound)
		default:
			t.Errorf("unknown mode %q", mode)
		}
	}))
	t.Cleanup(srv.Close)
	if srv.TLS != nil {
		trustTLS(t, srv)
	}
	base := srv.URL + "/" + secretToken + "/"
	return Asset{Name: geoipName, URL: base + geoipName}, Asset{Name: geositeName, URL: base + geositeName}
}

// trustTLS gives both clients a transport that trusts srv's test certificate.
func trustTLS(t *testing.T, srv *httptest.Server) {
	t.Helper()
	testTransport = srv.Client().Transport
	t.Cleanup(func() { testTransport = nil })
}

// assertGeoOutcome checks an install either put the new pair in or left the old
// one byte for byte, with nothing else left in dir either way.
func assertGeoOutcome(t *testing.T, dir string, err error, wantOK bool) {
	t.Helper()
	if wantOK {
		if err != nil {
			t.Fatalf("install refused: %v", err)
		}
		for _, db := range []string{geoipName, geositeName} {
			if got := readFile(t, filepath.Join(dir, db)); got != newGeo(db) {
				t.Errorf("%s = %q, want the new copy", db, got)
			}
		}
	} else {
		if err == nil {
			t.Fatal("install accepted")
		}
		assertOldGeo(t, dir)
	}
	assertDirHolds(t, dir, geoipName, geositeName)
}

func assertHidesToken(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("install accepted")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error shows the URL path: %v", err)
	}
}
