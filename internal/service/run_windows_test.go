//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("нужны права администратора")
	}
}

// trustOwners makes any owner, or none, the log folder's rightful one.
func trustOwners(t *testing.T, ok bool) {
	t.Helper()
	old := trustedOwner
	trustedOwner = func(*windows.SID) bool { return ok }
	t.Cleanup(func() { trustedOwner = old })
}

// dirSecurity is the owner of dir and whether its DACL is protected.
func dirSecurity(t *testing.T, dir string) (*windows.SID, bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	return owner, ctl&windows.SE_DACL_PROTECTED != 0
}

// A folder made for the log belongs to the administrators and inherits
// nothing; the service then opens it.
func TestLogDirMade(t *testing.T) {
	requireElevated(t)
	dir := filepath.Join(t.TempDir(), "log")
	if err := makeLogDir(dir); err != nil {
		t.Fatal(err)
	}
	owner, protected := dirSecurity(t, dir)
	if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		t.Errorf("owner %s, want the administrators", owner)
	}
	if !protected {
		t.Error("the DACL inherits from the parent")
	}
	h, err := openLogDir(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(h)
}

// A folder already there and rightfully owned is kept, with its DACL made the
// service's.
func TestLogDirExisting(t *testing.T) {
	requireElevated(t)
	trustOwners(t, true)
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeLogDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, protected := dirSecurity(t, dir); !protected {
		t.Error("the DACL of the folder already there was not replaced")
	}
}

// A folder of someone else's is neither written in nor taken over: the service
// does without its log, the install stops.
func TestLogDirForeignOwner(t *testing.T) {
	trustOwners(t, false)
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if h, err := openLogDir(dir, 0); err == nil {
		_ = windows.CloseHandle(h)
		t.Fatal("a folder of someone else's was taken for the log")
	}
	if err := makeLogDir(dir); err == nil {
		t.Fatal("the install took a folder of someone else's")
	}
	if _, protected := dirSecurity(t, dir); protected {
		t.Error("the DACL of a folder of someone else's was changed")
	}
}

// A junction in the folder's place is not followed: the service does not
// open it, the install removes the junction — not what it points to — and
// makes a folder of its own.
func TestLogDirJunction(t *testing.T) {
	trustOwners(t, true)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "log")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", dir, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v\n%s", err, out)
	}

	h, err := openLogDir(dir, 0)
	if err == nil {
		_ = windows.CloseHandle(h)
		t.Fatal("the junction was taken for the log folder")
	}
	if !errors.Is(err, errLogDirLink) {
		t.Errorf("the junction is refused for another reason: %v", err)
	}

	requireElevated(t)
	if err := makeLogDir(dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != "keep" {
		t.Errorf("what the junction pointed to was touched: %q, %v", b, err)
	}
	if _, protected := dirSecurity(t, target); protected {
		t.Error("the DACL of what the junction pointed to was changed")
	}
	h, err = openLogDir(dir, 0)
	if err != nil {
		t.Fatalf("the folder made in the junction's place: %v", err)
	}
	_ = windows.CloseHandle(h)
}
