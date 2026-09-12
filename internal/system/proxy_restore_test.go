//go:build windows

package system

import (
	"slices"
	"testing"
)

// TestRestoreDisabledProxy verifies that Restore writes ProxyEnable=0 when the
// original state was disabled. Regression for the bug where cleanup skipped
// restoration when oldProxy.Enabled == false, leaving 127.0.0.1:10809 active.
func TestRestoreDisabledProxy(t *testing.T) {
	f := useFakeRegistry(t, &fakeRegistry{
		ints: map[string]uint64{"ProxyEnable": 1},
		strs: map[string]string{"ProxyServer": "127.0.0.1:10809"},
	})

	pm := New()
	if err := pm.Restore(ProxyState{Read: true, EnabledSet: true, Enabled: false}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := f.ints["ProxyEnable"]; got != 0 {
		t.Errorf("ProxyEnable = %d, want 0", got)
	}
}

// TestRestoreEnabledProxy verifies that Restore writes ProxyEnable=1, the
// saved server, and the saved overrides when the state was enabled.
func TestRestoreEnabledProxy(t *testing.T) {
	f := useFakeRegistry(t, &fakeRegistry{})

	pm := New()
	if err := pm.Restore(ProxyState{
		Read:         true,
		EnabledSet:   true,
		Enabled:      true,
		ServerSet:    true,
		Server:       "1.2.3.4:80",
		OverridesSet: true,
		Overrides:    "localhost",
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := f.ints["ProxyEnable"]; got != 1 {
		t.Errorf("ProxyEnable = %d, want 1", got)
	}
	if got := f.strs["ProxyServer"]; got != "1.2.3.4:80" {
		t.Errorf("ProxyServer = %q, want %q", got, "1.2.3.4:80")
	}
	if got := f.strs["ProxyOverride"]; got != "localhost" {
		t.Errorf("ProxyOverride = %q, want %q", got, "localhost")
	}
}

// TestRestoreDeletesValuesThatDidNotExist is A12 itself: on a clean machine
// ProxyServer does not exist, and after a session it must not exist either.
func TestRestoreDeletesValuesThatDidNotExist(t *testing.T) {
	f := useFakeRegistry(t, &fakeRegistry{ints: map[string]uint64{"ProxyEnable": 0}})

	before := ReadProxyState()

	pm := New()
	if err := pm.Enable(10809); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, ok := f.strs["ProxyServer"]; !ok {
		t.Fatalf("Enable did not set ProxyServer")
	}

	if err := pm.Restore(before); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if _, ok := f.strs["ProxyServer"]; ok {
		t.Errorf("ProxyServer survived the session: %q", f.strs["ProxyServer"])
	}
	if _, ok := f.strs["ProxyOverride"]; ok {
		t.Errorf("ProxyOverride survived the session: %q", f.strs["ProxyOverride"])
	}
	if got := f.ints["ProxyEnable"]; got != 0 {
		t.Errorf("ProxyEnable = %d, want 0", got)
	}
	if !slices.Contains(f.deleted, "ProxyServer") {
		t.Errorf("expected ProxyServer to be deleted, deletions: %v", f.deleted)
	}
}

// TestRestoreRewritesValuesThatExisted is the other half: a corporate proxy
// configured before the session must come back byte for byte.
func TestRestoreRewritesValuesThatExisted(t *testing.T) {
	f := useFakeRegistry(t, &fakeRegistry{
		ints: map[string]uint64{"ProxyEnable": 1},
		strs: map[string]string{
			"ProxyServer":   "proxy.corp.local:3128",
			"ProxyOverride": "*.corp.local;<local>",
		},
	})

	before := ReadProxyState()

	pm := New()
	if err := pm.Enable(10809); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := pm.Restore(before); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := f.strs["ProxyServer"]; got != "proxy.corp.local:3128" {
		t.Errorf("ProxyServer = %q, want the corporate proxy back", got)
	}
	if got := f.strs["ProxyOverride"]; got != "*.corp.local;<local>" {
		t.Errorf("ProxyOverride = %q, want the original value back", got)
	}
	if got := f.ints["ProxyEnable"]; got != 1 {
		t.Errorf("ProxyEnable = %d, want 1", got)
	}
}

// TestRestoreRefusesUnreadState: restoring from a snapshot we never took would
// delete the user's real settings, so it is refused instead.
func TestRestoreRefusesUnreadState(t *testing.T) {
	f := useFakeRegistry(t, &fakeRegistry{
		ints: map[string]uint64{"ProxyEnable": 1},
		strs: map[string]string{"ProxyServer": "proxy.corp.local:3128"},
	})

	if err := New().Restore(ProxyState{}); err == nil {
		t.Fatalf("Restore accepted an unread state, want refusal")
	}
	if got := f.strs["ProxyServer"]; got != "proxy.corp.local:3128" {
		t.Errorf("refused Restore still touched the registry: ProxyServer = %q", got)
	}
	if len(f.deleted) != 0 {
		t.Errorf("refused Restore deleted %v", f.deleted)
	}
}
