//go:build linux

package secret

import (
	"bytes"
	"os"
	"testing"
)

// TestMain lets the test binary stand in for the program as the sudo helper:
// helperKey starts os.Executable with HelperArg alone.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == HelperArg {
		os.Exit(HelperMain())
	}
	os.Exit(m.Run())
}

// The real Secret Service, when one is running and unlocked:
//
//	dbus-run-session -- sh -c 'echo -n pw | gnome-keyring-daemon --unlock --components=secrets >/dev/null;
//	  XRAY_RUNNER_SECRET_SERVICE=1 go test -run LiveSecretService ./internal/secret/'
//
// Run as root with SUDO_UID and SUDO_GID of a user whose session bus is at
// /run/user/<uid>/bus, it goes through the helper instead.
func TestLiveSecretService(t *testing.T) {
	if os.Getenv("XRAY_RUNNER_SECRET_SERVICE") != "1" {
		t.Skip("set XRAY_RUNNER_SECRET_SERVICE=1 with a Secret Service running")
	}
	resetDefault(t)
	s, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	blob, err := s.Seal([]byte("token"))
	if err != nil {
		t.Fatal(err)
	}
	first := key

	// Another process — a fresh start — finds the same key.
	resetDefault(t)
	s, err = Default()
	if err != nil {
		t.Fatalf("Default again: %v", err)
	}
	if !bytes.Equal(first, key) {
		t.Fatal("a second start made another key")
	}
	if got, err := s.Open(blob); err != nil || string(got) != "token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	_, _, viaHelper := sudoUser()
	t.Logf("key from the Secret Service (helper: %v)", viaHelper)
}
