package app

import (
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
)

// isolateState gives a test its own working directory, its own data dir and a
// program directory of its own: legacy files are looked for next to the
// program (H02), and next to the real test binary there must be nothing to
// find. Both env vars: os.UserConfigDir reads XDG_CONFIG_HOME on Linux and
// AppData on Windows.
func isolateState(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", data)
	t.Setenv("AppData", data)
	placeProgram(t)
}

// placeProgram makes the program live in a fresh directory of the test's, and
// returns it.
func placeProgram(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := config.Executable
	config.Executable = func() (string, error) { return filepath.Join(dir, "xray-runner"), nil }
	t.Cleanup(func() { config.Executable = orig })
	return dir
}
