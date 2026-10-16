package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/safefile"
	"xray-runner/internal/subscription"
)

// stateBytes returns the raw content of the saved-state file.
func stateBytes(t *testing.T) []byte {
	t.Helper()
	data, err := safefile.ReadFile(statePath())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	return data
}

// writeLegacyState seeds a state file in the old format: the full subscription
// URL stored under "subscription_url".
func writeLegacyState(t *testing.T, subURL, remarks, address string, port int, mode string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"subscription_url": subURL,
		"server_remarks":   remarks,
		"server_address":   address,
		"server_port":      port,
		"mode":             mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := safefile.WriteFile(statePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// The state file is plain text even when the subscription list is sealed, so it
// may hold only a fingerprint of the subscription, never the link itself.
func TestRememberSelectionKeepsSecretsOutOfStateFile(t *testing.T) {
	tests := []struct {
		name   string
		subURL string
		secret string
	}{
		{"subscription", "https://panel.invalid/SYNTHETIC_TOKEN", "SYNTHETIC_TOKEN"},
		{"bare link", "vless://SYNTHETIC_UUID@host.invalid:443?type=tcp#name", "SYNTHETIC_UUID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateState(t)
			a := New(&config.Config{Mode: "proxy"}, Options{})
			a.rememberSelection(tt.subURL, &subscription.SubEntry{
				Remarks: "srv", Address: "host.invalid", Port: 443,
			})

			data := stateBytes(t)
			if strings.Contains(string(data), tt.secret) {
				t.Errorf("state file contains %q:\n%s", tt.secret, data)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			if got := raw["subscription_key"]; got != subKey(tt.subURL) {
				t.Errorf("subscription_key = %v, want %q", got, subKey(tt.subURL))
			}
		})
	}
}

// A file written by an older version is rewritten on first read without the
// link; the mode and the server survive.
func TestLoadLastStateMigratesLegacyURL(t *testing.T) {
	isolateState(t)
	const subURL = "https://panel.invalid/SYNTHETIC_TOKEN"
	writeLegacyState(t, subURL, "srv", "host.invalid", 443, "tun")

	s, err := loadLastState()
	if err != nil {
		t.Fatalf("loadLastState() = %v", err)
	}
	if s.Mode != "tun" || s.ServerRemarks != "srv" || s.ServerAddress != "host.invalid" || s.ServerPort != 443 {
		t.Errorf("state = %+v, want mode and server kept", s)
	}

	data := stateBytes(t)
	if strings.Contains(string(data), "SYNTHETIC_TOKEN") || strings.Contains(string(data), "subscription_url") {
		t.Errorf("state file still holds the legacy link:\n%s", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if got := raw["subscription_key"]; got != subKey(subURL) {
		t.Errorf("subscription_key = %v, want %q", got, subKey(subURL))
	}
	if raw["mode"] != "tun" || raw["server_address"] != "host.invalid" {
		t.Errorf("rewritten file lost data:\n%s", data)
	}

	// A second read changes nothing.
	again := stateBytes(t)
	if _, err := loadLastState(); err != nil {
		t.Fatal(err)
	}
	if string(stateBytes(t)) != string(again) {
		t.Error("migrated file changed on the second read")
	}
}

// profileServer serves panelProfile with the server hosts renamed, so the test
// can tell which of two subscriptions was actually fetched.
func renamedProfileServer(t *testing.T, hosts [2]string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	body := strings.NewReplacer("a.invalid", hosts[0], "b.invalid", hosts[1]).Replace(panelProfile)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[" + body + "]"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// twoSubscriptions sets up a list of two subscriptions and returns their URLs
// and hit counters.
func twoSubscriptions(t *testing.T) (urls [2]string, hits [2]*atomic.Int32) {
	t.Helper()
	isolateState(t)
	hits = [2]*atomic.Int32{new(atomic.Int32), new(atomic.Int32)}
	s1 := renamedProfileServer(t, [2]string{"a1.invalid", "b1.invalid"}, hits[0])
	s2 := renamedProfileServer(t, [2]string{"a2.invalid", "b2.invalid"}, hits[1])
	urls = [2]string{s1.URL + "/SYNTHETIC_TOKEN_1", s2.URL + "/SYNTHETIC_TOKEN_2"}
	list := urls[0] + "\n" + urls[1] + "\n"
	if err := os.WriteFile(config.Path("subscriptions.txt"), []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	return urls, hits
}

// --last after the migration picks the same subscription and server as before.
func TestLastAfterMigrationSelectsSameServer(t *testing.T) {
	urls, hits := twoSubscriptions(t)
	writeLegacyState(t, urls[1], "Auto", "b2.invalid", 443, "")

	a := New(&config.Config{Mode: "proxy"}, Options{UseLast: true, NonInteractive: true})
	tgt, err := a.resolveScriptedTarget(context.Background())
	if err != nil {
		t.Fatalf("resolveScriptedTarget: %v", err)
	}
	if tgt.subURL != urls[1] {
		t.Errorf("subURL = %q, want the second subscription", tgt.subURL)
	}
	if tgt.entry == nil || tgt.entry.Address != "b2.invalid" {
		t.Errorf("entry = %+v, want b2.invalid", tgt.entry)
	}
	if hits[0].Load() != 0 || hits[1].Load() == 0 {
		t.Errorf("hits = %d/%d, want only the second subscription fetched", hits[0].Load(), hits[1].Load())
	}
}

// Without --last, several subscriptions and no SUBSCRIPTION_URL, the saved
// choice decides; it is stored as a fingerprint and found in the list again.
func TestSavedChoiceFoundByFingerprint(t *testing.T) {
	urls, _ := twoSubscriptions(t)
	a := New(&config.Config{Mode: "proxy"}, Options{NonInteractive: true})
	a.rememberSelection(urls[1], &subscription.SubEntry{Remarks: "Auto", Address: "a2.invalid", Port: 443})

	tgt, err := a.resolveScriptedTarget(context.Background())
	if err != nil {
		t.Fatalf("resolveScriptedTarget: %v", err)
	}
	if tgt.subURL != urls[1] || tgt.entry == nil || tgt.entry.Address != "a2.invalid" {
		t.Errorf("target = %q %+v, want the second subscription, a2.invalid", tgt.subURL, tgt.entry)
	}
}

// A saved subscription that left the list is an error with a clear message, not
// a silent fallback to another one.
func TestSavedSubscriptionGoneIsSelectionError(t *testing.T) {
	for _, useLast := range []bool{true, false} {
		twoSubscriptions(t)
		a := New(&config.Config{Mode: "proxy"}, Options{UseLast: useLast, NonInteractive: true})
		a.rememberSelection("https://removed.invalid/SYNTHETIC_TOKEN_3", &subscription.SubEntry{
			Remarks: "Auto", Address: "a1.invalid", Port: 443,
		})

		_, err := a.resolveScriptedTarget(context.Background())
		if !errors.Is(err, ErrSelection) {
			t.Errorf("useLast=%v: err = %v, want ErrSelection", useLast, err)
			continue
		}
		if !strings.Contains(err.Error(), "сохранённой подписки больше нет в списке") {
			t.Errorf("useLast=%v: err = %q, want the missing-subscription message", useLast, err)
		}
	}
}
