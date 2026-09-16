//go:build windows

package app

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The one F07 check no ordinary run can make for itself (recheck-07, риск 3).
// Everything else reads the runtime dir's DACL back and compares it with what
// was asked for — which is reading a label from the account that wrote it. This
// opens the config as somebody else and sees what the system actually does.
//
// It needs a second unprivileged account to be somebody else with, and an
// elevated token to put that account's token on (SeImpersonatePrivilege).
// scripts/test-windows-f07.ps1 provides both and takes the account away again.
// Without them the test skips, so CI and a developer's machine stay unaffected.

// x/sys/windows wraps RevertToSelf but neither of these two.
var (
	advapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW              = advapi32.NewProc("LogonUserW")
	procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")
)

const (
	logon32LogonInteractive = 2
	logon32ProviderDefault  = 0
)

// secondUserCreds takes the helper account from the environment, or skips.
func secondUserCreds(t *testing.T) (name, pass string) {
	t.Helper()
	name, pass = os.Getenv("XRAY_RUNNER_SECOND_USER"), os.Getenv("XRAY_RUNNER_SECOND_PASS")
	if name == "" || pass == "" {
		t.Skip("нужна вторая обычная учётная запись: XRAY_RUNNER_SECOND_USER и XRAY_RUNNER_SECOND_PASS; их заводит scripts/test-windows-f07.ps1")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("подмена пользователя требует повышения: SeImpersonatePrivilege есть у администратора")
	}
	return name, pass
}

// logonUser signs the helper account in on this machine and returns its token.
// The domain is "." — a local account, never a domain one.
func logonUser(t *testing.T, name, pass string) windows.Token {
	t.Helper()
	u, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := windows.UTF16PtrFromString(".")
	if err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(pass)
	if err != nil {
		t.Fatal(err)
	}
	var token windows.Token
	//nolint:gosec // G103: the three strings and the out parameter outlive the call
	r, _, errno := procLogonUserW.Call(
		uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)),
		logon32LogonInteractive, logon32ProviderDefault, uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		t.Fatalf("вход под %q не удался: %v — проверьте имя и пароль второй учётной записи", name, errno)
	}
	t.Cleanup(func() { _ = token.Close() })
	return token
}

// asUser runs do with the thread wearing token's identity. The thread is locked
// for the duration: an impersonation belongs to a thread, and a goroutine that
// moved to another one would quietly go back to being the test's own user.
func asUser(t *testing.T, token windows.Token, do func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if r, _, errno := procImpersonateLoggedOnUser.Call(uintptr(token)); r == 0 {
		t.Fatalf("ImpersonateLoggedOnUser: %v", errno)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Carrying on as the other account would corrupt the cleanup.
			t.Fatalf("RevertToSelf: %v", err)
		}
	}()
	do()
}

// openable reports what happened when this identity tried to open path. A
// directory opens when its holder may list it, a file when they may read it.
func openable(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// F07, риск 3: the config carries the server's address, id and password. A
// second unprivileged account on the same machine must not be able to read it,
// whatever the temp dir above it allows.
func TestRuntimeDir_SecondUserIsLockedOut(t *testing.T) {
	name, pass := secondUserCreds(t)

	base := isolateRuntime(t)
	// A base the other account can walk into and read. Without it a refusal
	// somewhere above would pass for the runtime dir's own doing, and the test
	// would report a success it never earned.
	open := createDirWithSDDL(t, base, "open", "D:(A;OICI;FA;;;WD)")
	t.Setenv("TMP", open)
	t.Setenv("TEMP", open)

	a := ownClaim(t, t.TempDir())
	if err := a.writeConfigJSON(json.RawMessage(`{"log":{}}`)); err != nil {
		t.Fatalf("writeConfigJSON: %v", err)
	}

	// The control: a file in that same open base, made the ordinary way, so it
	// inherits the base's everyone-may-read entry.
	reachable := filepath.Join(open, "reachable.txt")
	if err := os.WriteFile(reachable, []byte("контрольный файл"), 0o600); err != nil {
		t.Fatal(err)
	}

	token := logonUser(t, name, pass)
	asUser(t, token, func() {
		if err := openable(reachable); err != nil {
			t.Fatalf("контроль не прошёл: вторая учётная запись не смогла прочитать даже открытый файл (%v). "+
				"Отказ ниже ничего не доказывает — проверьте, что учётная запись рабочая", err)
		}

		for _, tc := range []struct{ what, path string }{
			{"конфиг с учётными данными сервера", a.tmpFile},
			{"каталог рабочего конфига", a.runDir},
		} {
			err := openable(tc.path)
			switch {
			case err == nil:
				t.Errorf("ДЫРА: вторая учётная запись открыла %s: %s", tc.what, tc.path)
			case !errors.Is(err, fs.ErrPermission):
				t.Errorf("%s: отказ не по правам доступа, а %v — проверьте вручную", tc.what, err)
			}
		}

		// Reaching the config to delete it means opening the directory first,
		// which the entries above have just refused.
		if err := os.Remove(a.tmpFile); err == nil {
			t.Error("ДЫРА: вторая учётная запись удалила конфиг")
		}
	})

	// Not checked here: renaming the runtime dir itself. This base was made
	// deliberately open, so the other account holds FILE_DELETE_CHILD on it and
	// may rename what is inside — which is why a real run refuses a base like
	// this outright. That refusal is checkTrustedDirs' job and has its own tests
	// (TestClaim_RefusesUntrustedRuntimeBase, TestCheckTrustedSD).
}
