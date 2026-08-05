package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// Task #6: the name is cosmetic. A panel that is down, a token that has expired
// or no network at all must still leave the subscription in the list — it is
// picked up on the first open anyway.
func TestAddAndName_PanelFailureStillAddsTheSubscription(t *testing.T) {
	isolateState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "истёк срок подписки", http.StatusForbidden)
	}))
	defer srv.Close()

	a := New(&config.Config{}, Options{})
	if err := a.addAndName(srv.URL + "/sub"); err != nil {
		t.Fatalf("a failed name lookup must not fail the add: %v", err)
	}

	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		t.Fatalf("load subscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].URL != srv.URL+"/sub" {
		t.Fatalf("subscriptions = %+v, want the one just added", subs)
	}
}

// A panel that answers gets its name adopted straight away, so the list reads
// the panel's own name instead of the host in the URL from the first moment.
func TestAddAndName_AdoptsThePanelTitle(t *testing.T) {
	isolateState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("profile-title", "alohavpnbot")
		_, _ = w.Write([]byte("vless://123e4567-e89b-12d3-a456-426614174000@de1.example.ru:443?type=tcp&security=none#DE-1"))
	}))
	defer srv.Close()

	a := New(&config.Config{}, Options{})
	if err := a.addAndName(srv.URL + "/sub"); err != nil {
		t.Fatalf("addAndName: %v", err)
	}

	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		t.Fatalf("load subscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].Name != "alohavpnbot" {
		t.Fatalf("subscriptions = %+v, want the panel's own name", subs)
	}
}

// A bare vless:// link is its own server: there is no panel to ask, and asking
// would mean a pointless request to the proxy host itself.
func TestAddAndName_BareLinkAsksNobody(t *testing.T) {
	isolateState(t)

	const link = "vless://123e4567-e89b-12d3-a456-426614174000@nonexistent.invalid:8080?type=ws&security=none&path=%2Fws#GERMANY"
	a := New(&config.Config{}, Options{})
	if err := a.addAndName(link); err != nil {
		t.Fatalf("addAndName on a bare link: %v", err)
	}

	subs, err := subscription.LoadSubscriptions()
	if err != nil {
		t.Fatalf("load subscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].URL != link {
		t.Fatalf("subscriptions = %+v, want the bare link", subs)
	}
}
