package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// After a restart, a panel that does not answer still lets the user into the
// subscription: the menu opens the last saved answer, marks it stale for the
// list screen, and does not count it as fresh, so the next open asks again.
func TestLoadSubscriptionOpensSavedCopyWhenPanelIsDown(t *testing.T) {
	a, _ := newGeoApp(t)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + panelProfile + "]"))
	}))
	t.Cleanup(srv.Close)
	subURL := srv.URL + "/sub"

	if err := a.loadSubscription(context.Background(), subURL); err != nil {
		t.Fatalf("panel up: %v", err)
	}
	if a.nav.stale != nil || !a.nav.fresh(subURL) {
		t.Fatalf("fresh load: stale=%v fresh=%v", a.nav.stale, a.nav.fresh(subURL))
	}

	a.nav = nav{} // a new start: nothing in memory
	down.Store(true)
	if err := a.loadSubscription(context.Background(), subURL); err != nil {
		t.Fatalf("panel down: %v, want the saved copy", err)
	}
	if len(a.nav.profiles) != 1 || len(a.nav.profiles[0].Entries) != 2 {
		t.Errorf("profiles in hand: %+v", a.nav.profiles)
	}
	if a.nav.stale == nil {
		t.Error("the saved copy is not marked stale")
	}
	if a.nav.fresh(subURL) {
		t.Error("the saved copy counts as fresh — the next open would not ask the panel")
	}

	down.Store(false)
	if err := a.loadSubscription(context.Background(), subURL); err != nil {
		t.Fatal(err)
	}
	if a.nav.stale != nil {
		t.Error("the panel answered again, but the list is still marked stale")
	}
}
