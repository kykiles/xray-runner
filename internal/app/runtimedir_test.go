package app

// 11b: the config the core reads lives in a directory of this instance's own,
// made after the lock is taken — not at a path config.Path finds, where the
// working directory, or under sudo the user, gets a say.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// ownClaim claims the install in dir for the whole test.
func ownClaim(t *testing.T, dir string) *App {
	t.Helper()
	a := newLockApp(dir)
	if err := a.claimInstance(); err != nil {
		t.Fatalf("claimInstance: %v", err)
	}
	t.Cleanup(a.cleanup)
	return a
}

// sameDir compares two directories as the file system sees them: a temp base
// may be reached through a symlink (macOS /tmp), and the runtime dir is made
// under the resolved one.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// A config left where the old versions kept it — in the working directory,
// which config.Path prefers, or in the data dir — is neither where the core now
// reads its config nor overwritten by it (A04).
func TestClaim_ConfigGoesToPrivateRuntimeDir(t *testing.T) {
	isolateState(t)
	base := isolateRuntime(t)
	planted := []string{"xray_config.json", filepath.Join(config.DataDir(), "xray_config.json")}
	for _, p := range planted {
		writeFile(t, p, "planted")
	}

	a := New(&config.Config{}, Options{})
	if err := a.claimInstance(); err != nil {
		t.Fatalf("claimInstance: %v", err)
	}
	t.Cleanup(a.cleanup)
	if err := a.writeConfigJSON(json.RawMessage(`{"log":{}}`)); err != nil {
		t.Fatalf("writeConfigJSON: %v", err)
	}

	for _, p := range planted {
		assertFileHolds(t, p, "planted")
	}
	if a.runDir == "" || !sameDir(t, filepath.Dir(a.runDir), base) {
		t.Fatalf("runtime dir %q, want a new one in %q", a.runDir, base)
	}
	if filepath.Dir(a.tmpFile) != a.runDir {
		t.Errorf("config at %q, outside the runtime dir %q", a.tmpFile, a.runDir)
	}
	assertRuntimePrivate(t, a.runDir, a.tmpFile)
}

// An instance refused by the lock makes nothing: the runtime dir comes after
// the lock, and the owner's stays as it is — its core reads the config there
// again on every restart.
func TestClaim_RefusedInstanceMakesNoRuntimeDir(t *testing.T) {
	base := isolateRuntime(t)
	dir := t.TempDir()
	owner := ownClaim(t, dir)
	writeFile(t, owner.tmpFile, "active-instance-config")

	second := newLockApp(dir)
	if err := second.claimInstance(); err == nil {
		t.Fatal("claimInstance should refuse while another instance holds the lock")
	}
	if second.runDir != "" {
		t.Errorf("refused instance made a runtime dir %q", second.runDir)
	}
	second.cleanup()

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || owner.runDir == "" || !sameDir(t, filepath.Join(base, entries[0].Name()), owner.runDir) {
		t.Errorf("runtime base holds %d entries, want only the owner's %q", len(entries), owner.runDir)
	}
	assertFileHolds(t, owner.tmpFile, "active-instance-config")
}

// Each instance removes its own runtime dir and nothing else, however many
// times cleanup runs.
func TestCleanup_RemovesOnlyItsOwnRuntimeDir(t *testing.T) {
	base := isolateRuntime(t)
	a := ownClaim(t, t.TempDir())
	b := ownClaim(t, t.TempDir())
	if a.runDir == b.runDir {
		t.Fatalf("two instances share the runtime dir %q", a.runDir)
	}
	writeFile(t, a.tmpFile, "a-config")
	writeFile(t, b.tmpFile, "b-config")
	aDir := a.runDir

	a.cleanup()
	a.cleanup()

	if _, err := os.Lstat(aDir); !os.IsNotExist(err) {
		t.Errorf("owner's runtime dir left behind after cleanup (lstat err = %v)", err)
	}
	assertFileHolds(t, b.tmpFile, "b-config")
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		t.Errorf("runtime base touched by cleanup (stat err = %v)", err)
	}
}

// The bench configs are read by the same core as the session's, so they go to
// the instance's runtime dir too, not to the temp dir the environment names.
func TestRunBatch_ConfigsGoToRuntimeDir(t *testing.T) {
	isolateRuntime(t)
	runDir := t.TempDir()
	pb := &ProxyBenchmarker{concurrency: 1, runDir: runDir}
	var got string
	runBatch(context.Background(), pb, []int{0},
		func(_ context.Context, _ int, _ portPair, dir string) subscription.BenchmarkResult {
			got = dir
			return subscription.BenchmarkResult{}
		}, nil)
	if filepath.Dir(got) != runDir {
		t.Errorf("bench configs in %q, want a dir in the runtime dir %q", got, runDir)
	}
}
