package subscription

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsBareLink(t *testing.T) {
	bare := []string{
		"vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080?type=ws#srv",
		"ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8443#myserver",
		"hysteria2://pass@hy.example.com:443#hy",
		"hy2://pass@hy.example.com:443#hy",
		"trojan://pass@example.com:443?security=tls&sni=a.example.com#tr",
		"vmess://" + vmessB64(t),
	}
	for _, s := range bare {
		if !IsBareLink(s) {
			t.Errorf("IsBareLink(%q) = false, want true", s)
		}
	}

	notBare := []string{
		"https://sub.example.com/token",
		"http://sub.example.com/token",
		"example.com/sub",
		"",
	}
	for _, s := range notBare {
		if IsBareLink(s) {
			t.Errorf("IsBareLink(%q) = true, want false", s)
		}
	}
}

func TestParseBareLink(t *testing.T) {
	e, err := ParseBareLink("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080?type=ws&path=%2Fws#GERMANY%203")
	if err != nil {
		t.Fatalf("ParseBareLink: %v", err)
	}
	if e.Protocol != "vless" {
		t.Errorf("Protocol = %q, want vless", e.Protocol)
	}
	if e.Address != "example.com" || e.Port != 8080 {
		t.Errorf("endpoint = %s:%d, want example.com:8080", e.Address, e.Port)
	}
	if e.Remarks != "GERMANY 3" {
		t.Errorf("Remarks = %q, want %q", e.Remarks, "GERMANY 3")
	}
}

func TestParseBareLink_RejectsSubscriptionURL(t *testing.T) {
	if _, err := ParseBareLink("https://sub.example.com/token"); err == nil {
		t.Error("ParseBareLink on https URL: expected error, got nil")
	}
}

// A bare link must never trigger an HTTP request: the test URL points at a host
// that does not resolve, so a fetch attempt would fail the test.
func TestFetchProfiles_BareLinkNoNetwork(t *testing.T) {
	profiles, err := FetchProfiles("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@nonexistent.invalid:8080?type=ws#solo")
	if err != nil {
		t.Fatalf("FetchProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(profiles))
	}
	if len(profiles[0].Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(profiles[0].Entries))
	}
	if profiles[0].Name != "" {
		t.Errorf("Name = %q, want empty so the profile screen is skipped", profiles[0].Name)
	}
	if profiles[0].Raw != nil {
		t.Error("Raw must be nil so the target runs as a single server, not a profile")
	}
	if got := profiles[0].Entries[0].Remarks; got != "solo" {
		t.Errorf("Remarks = %q, want solo", got)
	}
}

func TestFetch_BareLinkNoNetwork(t *testing.T) {
	entries, err := Fetch("ss://YWVzLTI1Ni1nY206c2VjcmV0@nonexistent.invalid:8443#myserver")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Protocol != "ss" {
		t.Errorf("Protocol = %q, want ss", entries[0].Protocol)
	}
}

// The menu label for a bare link comes from its #fragment; falling back to the
// URL host would render vmess base64 as garbage.
func TestLoadSubscriptions_BareLinkName(t *testing.T) {
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	link := "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080?type=ws#GERMANY%203"
	if err := SaveSubscription(link); err != nil {
		t.Fatalf("SaveSubscription: %v", err)
	}
	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subs = %d, want 1", len(subs))
	}
	if subs[0].Name != "GERMANY 3" {
		t.Errorf("Name = %q, want %q", subs[0].Name, "GERMANY 3")
	}
	if subs[0].URL != link {
		t.Errorf("URL = %q, want the link verbatim", subs[0].URL)
	}
}

// Without a #fragment there is nothing to name the entry after, so the endpoint
// is used instead of the raw line (which would leak the UUID into the menu).
func TestLoadSubscriptions_BareLinkNameFallback(t *testing.T) {
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	if err := SaveSubscription("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080?type=ws"); err != nil {
		t.Fatalf("SaveSubscription: %v", err)
	}
	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if subs[0].Name != "example.com:8080" {
		t.Errorf("Name = %q, want example.com:8080", subs[0].Name)
	}
}

// An explicit # comment above the link still wins, like it does for http subs.
func TestLoadSubscriptions_BareLinkCommentWins(t *testing.T) {
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	body := "# Домашний\nvless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.com:8080#GERMANY\n"
	if err := os.WriteFile(subscriptionsFile, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if subs[0].Name != "Домашний" {
		t.Errorf("Name = %q, want Домашний", subs[0].Name)
	}
}

func vmessB64(t *testing.T) string {
	t.Helper()
	obj := map[string]interface{}{
		"v": "2", "ps": "vm", "add": "example.com", "port": "443",
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "net": "ws",
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal vmess: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// A truncated or mistyped vmess link must fail at parse time. Silently
// accepting it makes IsBareLink disagree with ParseBareLink and defers the
// failure to Validate, long after the link was saved.
func TestParseBareLink_RejectsBrokenVMess(t *testing.T) {
	for _, raw := range []string{
		"vmess://not-base64!!!", // not base64 at all
		"vmess://bm90LWpzb24",   // decodes to "not-json"
		"vmess://e30",           // decodes to "{}" — no address
	} {
		if IsBareLink(raw) {
			t.Errorf("IsBareLink(%q) = true, want false", raw)
		}
		if _, err := ParseBareLink(raw); err == nil {
			t.Errorf("ParseBareLink(%q) = nil error, want error", raw)
		}
	}
}
