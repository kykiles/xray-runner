package subscription

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/secret"
)

const token = "https://panel.example/sub/SECRET-TOKEN"

func withSealer(t *testing.T, f func() (secret.Sealer, error)) {
	t.Helper()
	orig := sealer
	sealer = f
	t.Cleanup(func() { sealer = orig })
}

func withFile(t *testing.T) (plain, sealed string) {
	t.Helper()
	dir := t.TempDir()
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(dir, plainName)
	t.Cleanup(func() { subscriptionsFile = orig })
	return subscriptionsFile, filepath.Join(dir, sealedName)
}

func xor7() (secret.Sealer, error) { return xorSealer{key: 7}, nil }

// A plain list from before is sealed on the first read, and the plain copy
// with the tokens in it is gone.
func TestSubscriptionsAreSealedOnFirstRead(t *testing.T) {
	plain, sealed := withFile(t)
	withSealer(t, xor7)
	if err := os.WriteFile(plain, []byte("# Панель\n"+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	subs, err := LoadSubscriptions()
	if err != nil || len(subs) != 1 || subs[0].URL != token || subs[0].Name != "Панель" {
		t.Fatalf("LoadSubscriptions = %+v, %v", subs, err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Errorf("the plain list is still there: %v", err)
	}
	blob, err := os.ReadFile(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !secret.Sealed(blob) || strings.Contains(string(blob), "SECRET-TOKEN") {
		t.Errorf("sealed file %q is not sealed", blob)
	}
	if subs, err := LoadSubscriptions(); err != nil || len(subs) != 1 {
		t.Errorf("reading back the sealed list: %+v, %v", subs, err)
	}
}

// Every change keeps the list sealed; no plain copy appears on the way.
func TestSubscriptionChangesStaySealed(t *testing.T) {
	plain, sealed := withFile(t)
	withSealer(t, xor7)
	for _, u := range []string{token, token + "2", token + "3"} {
		if err := SaveSubscription(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := NameSubscription(token+"2", "Вторая"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSubscription(0); err != nil {
		t.Fatal(err)
	}
	subs, err := LoadSubscriptions()
	if err != nil || len(subs) != 2 || subs[0].Name != "Вторая" {
		t.Fatalf("list after the changes: %+v, %v", subs, err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Errorf("a plain list appeared: %v", err)
	}
	if fi, err := os.Stat(sealed); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("sealed file: %v, %v; want 0600", fi, err)
	}
}

// A sealed list whose key is gone is an error, not an empty list — and nothing
// is written over it: the next save would otherwise replace every
// subscription with the one being added.
func TestSealedSubscriptionsWithoutTheKey(t *testing.T) {
	_, sealed := withFile(t)
	withSealer(t, xor7)
	if err := SaveSubscription(token); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(sealed)

	for name, f := range map[string]func() (secret.Sealer, error){
		"no key store": noSealer,
		"another key":  func() (secret.Sealer, error) { return badKey{}, nil },
	} {
		withSealer(t, f)
		if _, err := LoadSubscriptions(); err == nil {
			t.Errorf("%s: the list loaded", name)
		}
		if err := SaveSubscription(token + "new"); err == nil {
			t.Errorf("%s: a save went through", name)
		}
		if after, _ := os.ReadFile(sealed); string(after) != string(before) {
			t.Errorf("%s: the sealed file changed", name)
		}
	}
}

type badKey struct{ xorSealer }

func (badKey) Open([]byte) ([]byte, error) { return nil, os.ErrInvalid }

// Without a key store the list stays as it always was — plain, 0600 — and the
// screen hears why.
func TestSubscriptionsWithoutAKeyStore(t *testing.T) {
	plain, sealed := withFile(t)
	withSealer(t, noSealer)
	if err := SaveSubscription(token); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(plain); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("plain file: %v, %v; want 0600", fi, err)
	}
	if _, err := os.Stat(sealed); !os.IsNotExist(err) {
		t.Errorf("a sealed file appeared without a key store")
	}
	if note := StorageNote(); !strings.Contains(note, "без шифрования") {
		t.Errorf("StorageNote = %q", note)
	}
	withSealer(t, xor7)
	if note := StorageNote(); note != "" {
		t.Errorf("StorageNote with a key store = %q, want nothing", note)
	}
}

// A list next to the program is the portable install and stays plain: a key
// bound to this machine would not open it on the next. An empty file there —
// what a bundle ships — is not one, and the data dir is used.
func TestPortableSubscriptionsStayPlain(t *testing.T) {
	withSealer(t, xor7)
	prog := t.TempDir()
	orig := config.Executable
	config.Executable = func() (string, error) { return filepath.Join(prog, "xray-runner"), nil }
	t.Cleanup(func() { config.Executable = orig })
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)
	next := filepath.Join(prog, plainName)

	if err := os.WriteFile(next, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSubscription(token); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(next); len(b) != 0 {
		t.Errorf("the empty placeholder next to the program was written: %q", b)
	}
	if _, err := os.Stat(filepath.Join(data, "xray-runner", sealedName)); err != nil {
		t.Errorf("no sealed list in the data dir: %v", err)
	}

	if err := os.WriteFile(next, []byte(token+"portable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSubscription(token + "2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(next)
	if !strings.Contains(string(b), token+"2") || secret.Sealed(b) {
		t.Errorf("portable list = %q, want it plain with the new entry", b)
	}
	if note := StorageNote(); note != "" {
		t.Errorf("StorageNote in portable mode = %q", note)
	}
}
