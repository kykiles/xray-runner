package updater

import (
	"errors"
	"testing"
	"time"
)

// Task #4: bouncing in and out of the update screen must not re-query GitHub.
func TestCachedReusesWithinTTL(t *testing.T) {
	calls := 0
	fetch := func() (int, error) { calls++; return calls, nil }

	var slot int
	var at time.Time
	if v, _ := cached(&slot, &at, fetch); v != 1 {
		t.Fatalf("first lookup = %d, want 1", v)
	}
	if v, _ := cached(&slot, &at, fetch); v != 1 || calls != 1 {
		t.Errorf("second lookup = %d after %d fetches, want the cached 1", v, calls)
	}

	at = time.Now().Add(-2 * cacheTTL)
	if v, _ := cached(&slot, &at, fetch); v != 2 {
		t.Errorf("expired lookup = %d, want a fresh 2", v)
	}

	// A failed fetch must not be remembered as the answer.
	at = time.Time{}
	boom := func() (int, error) { return 0, errors.New("нет сети") }
	if _, err := cached(&slot, &at, boom); err == nil {
		t.Fatal("failed fetch returned no error")
	}
	if v, _ := cached(&slot, &at, fetch); v != 3 {
		t.Errorf("after a failure the next lookup = %d, want a fresh 3", v)
	}
}

// The geo databases of a release already installed in this run are not
// downloaded a second time.
func TestGeoInstalledMemory(t *testing.T) {
	if GeoInstalled("v202607") {
		t.Fatal("nothing installed yet")
	}
	MarkGeoInstalled("v202607")
	if !GeoInstalled("v202607") {
		t.Error("the installed release was not remembered")
	}
	if GeoInstalled("v202608") || GeoInstalled("") {
		t.Error("another (or an unknown) release must not count as installed")
	}
}
