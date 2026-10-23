package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"xray-runner/internal/secret"
)

const cachedLink = "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@cached.invalid:443?type=tcp&security=tls&sni=cached.invalid#cached"

// withCacheDir gives a test a cache of its own.
func withCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := cacheDir
	cacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheDir = orig })
	return dir
}

// flakyPanel serves cachedLink until down is set, then answers 502.
func flakyPanel(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Profile-Title", "Панель")
		_, _ = w.Write([]byte(cachedLink + "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, &down
}

// A panel that stops answering is stood in for by its last good answer, and
// the caller is told how old it is and why the fetch failed.
func TestFetchProfilesCachedFallsBackOnFailure(t *testing.T) {
	withCacheDir(t)
	srv, down := flakyPanel(t)
	subURL := srv.URL + "/sub/TOKEN"

	profiles, info, stale, err := FetchProfilesCached(context.Background(), subURL)
	if err != nil || stale != nil || len(Flatten(profiles)) != 1 {
		t.Fatalf("fresh fetch: %d servers, stale=%v, err=%v", len(Flatten(profiles)), stale, err)
	}
	if info.Title != "Панель" {
		t.Fatalf("fresh title = %q", info.Title)
	}

	down.Store(true)
	profiles, info, stale, err = FetchProfilesCached(context.Background(), subURL)
	if err != nil {
		t.Fatalf("panel down: %v, want the saved copy", err)
	}
	if stale == nil || stale.Err == nil || stale.At.IsZero() {
		t.Fatalf("stale = %+v, want the fetch error and the copy's time", stale)
	}
	if e := Flatten(profiles); len(e) != 1 || e[0].Address != "cached.invalid" {
		t.Errorf("saved copy gave %+v", e)
	}
	if info.Title != "Панель" {
		t.Errorf("saved title = %q, want the panel's", info.Title)
	}
}

// Without a saved answer the failure is the caller's to see.
func TestFetchProfilesCachedWithoutACopy(t *testing.T) {
	withCacheDir(t)
	srv, down := flakyPanel(t)
	down.Store(true)

	if _, _, stale, err := FetchProfilesCached(context.Background(), srv.URL+"/sub/TOKEN"); err == nil || stale != nil {
		t.Fatalf("stale=%v err=%v, want the fetch error", stale, err)
	}
}

// A fetch the user stopped (backed out of the wait) is not the panel failing:
// no saved copy is opened in its place.
func TestFetchProfilesCachedCancelled(t *testing.T) {
	withCacheDir(t)
	srv, _ := flakyPanel(t)
	subURL := srv.URL + "/sub/TOKEN"
	if _, _, _, err := FetchProfilesCached(context.Background(), subURL); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if profiles, _, stale, err := FetchProfilesCached(ctx, subURL); err == nil || stale != nil || profiles != nil {
		t.Fatalf("cancelled fetch: %d profiles, stale=%v, err=%v", len(profiles), stale, err)
	}
}

// The copy holds every server's credentials, so it is sealed the way the list
// is, and kept owner-only.
func TestCachedAnswerIsSealed(t *testing.T) {
	dir := withCacheDir(t)
	withSealer(t, xor7)
	srv, down := flakyPanel(t)
	subURL := srv.URL + "/sub/TOKEN"
	if _, _, _, err := FetchProfilesCached(context.Background(), subURL); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("cache dir: %v, %v", files, err)
	}
	if strings.Contains(files[0].Name(), "TOKEN") {
		t.Errorf("file name %q carries the token", files[0].Name())
	}
	path, _ := cachePath(subURL)
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !secret.Sealed(blob) || strings.Contains(string(blob), "aaaaaaaa-bbbb") {
		t.Errorf("the copy is not sealed: %q", blob)
	}
	if fi, err := os.Stat(path); err != nil || !ownerOnly(fi) {
		t.Errorf("mode %v, want 0600", fi.Mode())
	}

	down.Store(true)
	if _, _, stale, err := FetchProfilesCached(context.Background(), subURL); err != nil || stale == nil {
		t.Errorf("sealed copy not opened: stale=%v err=%v", stale, err)
	}
}

// Removing a subscription takes its saved answer with it.
func TestRemoveSubscriptionDropsItsCopy(t *testing.T) {
	withCacheDir(t)
	withFile(t)
	srv, _ := flakyPanel(t)
	subURL := srv.URL + "/sub/TOKEN"
	if err := SaveSubscription(subURL); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := FetchProfilesCached(context.Background(), subURL); err != nil {
		t.Fatal(err)
	}
	path, _ := cachePath(subURL)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no copy saved: %v", err)
	}

	if err := RemoveSubscription(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the copy outlived its subscription: %v", err)
	}
}
