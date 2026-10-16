package app

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/updater"
)

// watchdog turns a missed signal into a failure instead of a hung test.
const watchdog = 10 * time.Second

// geoPanel serves one subscription: panelProfile with its two servers renamed
// to hosts, and a Routing header that points the geo databases at geoHost.
func geoPanel(t *testing.T, hosts [2]string, geoHost string) *httptest.Server {
	t.Helper()
	body := strings.NewReplacer("a.invalid", hosts[0], "b.invalid", hosts[1]).Replace(panelProfile)
	routing := `{"Geoipurl":"https://` + geoHost + `/geoip.dat","Geositeurl":"https://` + geoHost + `/geosite.dat"}`
	header := "happ://routing/add/" + base64.StdEncoding.EncodeToString([]byte(routing))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Routing", header)
		_, _ = w.Write([]byte("[" + body + "]"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Opening subscription A, backing out and opening B must leave B in place, even
// when A's work is still in flight and finishes after B has been loaded: a
// cancelled load changes neither the profiles in hand nor the geo databases in
// use.
func TestCancelledLoadDoesNotOverwriteLaterOne(t *testing.T) {
	a, _ := newGeoApp(t)
	srvA := geoPanel(t, [2]string{"a-1.invalid", "a-2.invalid"}, "geo-a.invalid")
	srvB := geoPanel(t, [2]string{"b-1.invalid", "b-2.invalid"}, "geo-b.invalid")
	urlA, urlB := srvA.URL+"/sub-a", srvB.URL+"/sub-b"
	panelDir := func(u string) string { return filepath.Join(config.CacheDir(), "geo", subKey(u)) }

	var (
		started   = make(chan struct{}) // A's geo download has begun
		sawCancel = make(chan struct{}) // ... and has seen its context cancelled
		release   = make(chan struct{}) // lets A's download finish
	)
	orig := installPanelGeo
	t.Cleanup(func() { installPanelGeo = orig })
	installPanelGeo = func(ctx context.Context, geoip, _ updater.Asset, dir string) error {
		if !strings.Contains(geoip.URL, "geo-a.invalid") {
			writeGeoPair(t, dir, []string{"google"}, time.Now())
			return nil
		}
		close(started)
		select {
		case <-ctx.Done():
			close(sawCancel)
			<-release // A winds down only after B is fully loaded
			return ctx.Err()
		case <-release:
			// Never cancelled: the download finishes late and succeeds.
			writeGeoPair(t, dir, []string{"google"}, time.Now())
			return nil
		}
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release) // never leave A's download parked
		}
	})

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	errA := make(chan error, 1)
	go func() { errA <- a.loadSubscription(ctxA, urlA) }()

	select {
	case <-started:
	case <-time.After(watchdog):
		t.Fatal("A's geo download never started")
	}
	cancelA()

	// (1) The download in flight is told about the cancellation.
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Error("A's geo download never saw its context cancelled")
	}

	if err := a.loadSubscription(context.Background(), urlB); err != nil {
		t.Fatalf("loading B: %v", err)
	}
	close(release)

	// (2) A reports the cancellation instead of success.
	select {
	case err := <-errA:
		if err == nil {
			t.Error("loading A returned nil after it was cancelled")
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("loading A: %v, want context.Canceled", err)
		}
	case <-time.After(watchdog):
		t.Fatal("loading A never returned")
	}

	// (3) Everything in hand is B's.
	if a.nav.loadedURL != urlB {
		t.Errorf("loaded subscription = %q, want B", a.nav.loadedURL)
	}
	if len(a.nav.profiles) != 1 || len(a.nav.profiles[0].Entries) != 2 || a.nav.profiles[0].Entries[0].Address != "b-1.invalid" {
		t.Errorf("profiles in hand are not B's (%d profiles)", len(a.nav.profiles))
	}
	if want := panelDir(urlB); a.geoDir != want {
		t.Errorf("geoDir = %s, want %s", a.geoDir, want)
	}
	if want := panelDir(urlB); os.Getenv("XRAY_LOCATION_ASSET") != want {
		t.Errorf("XRAY_LOCATION_ASSET = %s, want %s", os.Getenv("XRAY_LOCATION_ASSET"), want)
	}
}

// commitLoad applies the result of a load only while that load is still wanted.
func TestCommitLoad(t *testing.T) {
	a := &App{}
	applied := 0

	ctx, cancel := context.WithCancel(context.Background())
	if !a.commitLoad(ctx, func() { applied++ }) || applied != 1 {
		t.Fatalf("live context: applied = %d, want 1", applied)
	}
	cancel()
	if a.commitLoad(ctx, func() { applied++ }) || applied != 1 {
		t.Fatalf("cancelled context: applied = %d, want 1 (nothing applied)", applied)
	}
}
