//go:build !windows

package log

import (
	"os"
	"path/filepath"
	"testing"

	"xray-runner/internal/config"
)

// asSudo makes the log package see a sudo run by uid.
func asSudo(t *testing.T, uid int) {
	t.Helper()
	orig := sudoOwner
	sudoOwner = func() (int, int, bool) { return uid, uid, true }
	t.Cleanup(func() { sudoOwner = orig })
}

// otherUID is a uid that owns nothing the test creates.
func otherUID() int { return os.Geteuid() + 4242 }

func initLog(t *testing.T, path string) {
	t.Helper()
	cleanup := Init(&config.Config{LogEnabled: true, LogFile: path, LogLevel: "info"})
	t.Cleanup(cleanup)
}

func writeVictim(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertIntact(t *testing.T, path string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "must survive" {
		t.Errorf("%s was written through: %q", path, got)
	}
}

// LOG_FILE comes from the .env of whatever directory sudo was run in, so it can
// name any file. One that belongs neither to the sudo user nor to a directory of
// theirs is not truncated — and so never handed to them either (G01).
func TestInit_SudoLeavesAForeignFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "victim")
	writeVictim(t, path)
	asSudo(t, otherUID())

	initLog(t, path)

	assertIntact(t, path)
}

// Nor does it create a file in a directory that is not the sudo user's: the
// file would be theirs after the handback, planted wherever LOG_FILE says.
func TestInit_SudoCreatesNothingInAForeignDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray-runner.log")
	asSudo(t, otherUID())

	initLog(t, path)

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("log created in a directory the sudo user does not own: %v", err)
	}
}

// A second hard link to a file of the user's own makes the truncate reach the
// other name too.
func TestInit_SudoRefusesAHardLinkedLog(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	writeVictim(t, victim)
	path := filepath.Join(dir, "xray-runner.log")
	if err := os.Link(victim, path); err != nil {
		t.Skipf("hard link: %v", err)
	}
	asSudo(t, os.Geteuid())

	initLog(t, path)

	assertIntact(t, victim)
}

// The ordinary case still works: the data dir is the user's, and the log there
// is emptied and written.
func TestInit_SudoWritesTheUsersOwnLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray-runner.log")
	writeVictim(t, path)
	asSudo(t, os.Geteuid())

	initLog(t, path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("log kept the previous run: %q", got)
	}
}
