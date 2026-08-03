package app

import "testing"

// isolateState gives a test its own working directory *and* its own data dir.
// State no longer lives next to the binary, so chdir alone would let the state
// file leak between tests — and into the developer's real profile.
// Both env vars: os.UserConfigDir reads XDG_CONFIG_HOME on Linux and AppData on
// Windows.
func isolateState(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)
}
