//go:build linux

package secret

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeKeyring is the Secret Service as a map.
func fakeKeyring(t *testing.T) map[string]string {
	t.Helper()
	store := map[string]string{}
	origGet, origSet := keyringGet, keyringSet
	keyringGet = func(svc, acct string) (string, error) {
		v, ok := store[svc+"/"+acct]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	keyringSet = func(svc, acct, v string) error { store[svc+"/"+acct] = v; return nil }
	t.Cleanup(func() { keyringGet, keyringSet = origGet, origSet })
	return store
}

func resetDefault(t *testing.T) {
	t.Helper()
	keyOnce, key, keyErr = sync.Once{}, nil, nil
	t.Cleanup(func() { keyOnce, key, keyErr = sync.Once{}, nil, nil })
}

func TestLocalKeyIsMadeOnceAndKept(t *testing.T) {
	store := fakeKeyring(t)
	k1, err := localKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(store) != 1 {
		t.Fatalf("keyring holds %v", store)
	}
	k2, err := localKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("a second read made another key")
	}
}

func TestLocalKeyRefusesADamagedKey(t *testing.T) {
	store := fakeKeyring(t)
	store[service+"/"+account] = "c2hvcnQ="
	if _, err := localKey(); err == nil {
		t.Error("a 5-byte key was taken")
	}
}

func TestSealRoundTripAndTamper(t *testing.T) {
	fakeKeyring(t)
	resetDefault(t)
	s, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("https://panel.example/sub/token\n")
	blob, err := s.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !Sealed(blob) || bytes.Contains(blob, []byte("token")) {
		t.Fatalf("sealed blob %q shows the plain text or lacks the header", blob)
	}
	got, err := s.Open(blob)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := s.Open(blob); err == nil {
		t.Error("a tampered blob opened")
	}
	if _, err := s.Open(plain); err == nil {
		t.Error("plain text opened as sealed")
	}
	if _, err := s.Open(wrap("dpapi", []byte("x"))); err == nil {
		t.Error("a DPAPI blob opened with the Secret Service key")
	}
}

// A headless machine has no Secret Service: Default says so, as ErrUnavailable.
func TestDefaultUnavailable(t *testing.T) {
	resetDefault(t)
	orig := fetchKey
	fetchKey = func() ([]byte, error) { return nil, errors.New("dbus: DBUS_SESSION_BUS_ADDRESS not set") }
	t.Cleanup(func() { fetchKey = orig })
	if _, err := Default(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Default = %v, want ErrUnavailable", err)
	}
}
