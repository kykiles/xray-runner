//go:build windows

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isolateRuntime points the runtime base at a fresh directory of the test's, as
// an ordinary run would take it, even from an elevated shell. The base's trust
// check is stood down with it: what the machine's temp dir inherited is not
// what these tests are about, and it has tests of its own below.
func isolateRuntime(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("TMP", base)
	t.Setenv("TEMP", base)
	origElevated, origCheck := elevated, checkRuntimeBase
	elevated = func() bool { return false }
	checkRuntimeBase = func(string, *windows.SID) error { return nil }
	t.Cleanup(func() { elevated, checkRuntimeBase = origElevated, origCheck })
	return base
}

// testUserACL is the ordinary run's ACL, as the running test's own token gives
// it.
func testUserACL(t *testing.T) runtimeACL {
	t.Helper()
	acl, err := userRuntimeACL()
	if err != nil {
		t.Fatalf("userRuntimeACL: %v", err)
	}
	return acl
}

// assertRuntimePrivate: the runtime dir carries the rights it was created with
// and none from the temp dir above, and so do the config in it and a bench
// dir's config underneath — those get theirs by inheriting the dir's.
func assertRuntimePrivate(t *testing.T, dir, file string) {
	t.Helper()
	acl := testUserACL(t)

	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	if err := checkProtectedSD(sd, acl); err != nil {
		t.Errorf("%s: %v", dir, err)
	}

	// The bench configs go to a dir of their own under the runtime dir
	// (ProxyBenchmarker.runDir), so they are covered by the same check.
	benchDir, err := os.MkdirTemp(dir, "xray-bench-*")
	if err != nil {
		t.Fatal(err)
	}
	benchConfig := filepath.Join(benchDir, "xray_config.json")
	if err := replaceFile(benchConfig, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, benchDir, benchConfig} {
		assertPrivateTo(t, path, acl)
	}
}

// assertPrivateTo checks one file or directory inside the runtime dir: nobody
// but this user, SYSTEM and Administrators owns it or appears in its DACL.
// Only the runtime dir itself carries SE_DACL_PROTECTED — what is inside is
// meant to inherit from it — so that part is checkProtectedSD's alone.
func assertPrivateTo(t *testing.T, path string, acl runtimeACL) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !sidIn(owner, acl.allowed) {
		t.Errorf("%s: владелец %s", path, owner)
	}
	aces, err := daclEntries(sd)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(aces) == 0 {
		t.Fatalf("%s: пустой DACL — доступ не задан", path)
	}
	for _, e := range aces {
		if e.typ != windows.ACCESS_ALLOWED_ACE_TYPE || !sidIn(e.sid, acl.allowed) {
			t.Errorf("%s: в DACL запись типа %d для %s", path, e.typ, e.sid)
		}
	}
}

func sdFromString(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("%s: %v", sddl, err)
	}
	return sd
}

// createDirWithSDDL makes a directory whose DACL is sddl, standing in for a
// temp dir a machine was set up with. It is made inside the test's own tree:
// the system's temp dir is never re-permissioned.
func createDirWithSDDL(t *testing.T, parent, name, sddl string) string {
	t.Helper()
	sa := windows.SecurityAttributes{SecurityDescriptor: sdFromString(t, sddl)}
	sa.Length = uint32(unsafe.Sizeof(sa))
	dir := filepath.Join(parent, name)
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(p, &sa); err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Whether a directory on the way to the config is trusted comes down to what a
// process the run does not trust may do to it.
func TestCheckTrustedSD(t *testing.T) {
	for name, tc := range map[string]struct {
		sddl string
		ok   bool
	}{
		// C:\Windows\Temp as installed: users may add entries, not remove them.
		"windows temp":              {"O:SYD:P(A;OICIIO;FA;;;CO)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;CI;0x100026;;;BU)", true},
		"trustedinstaller owner":    {"O:" + trustedInstallerSID + "D:(A;;FA;;;SY)", true},
		"inherit-only for users":    {"O:SYD:(A;OICIIO;GA;;;BU)(A;;FA;;;SY)", true},
		"deny for users":            {"O:SYD:(D;;FA;;;BU)(A;;FA;;;SY)", true},
		"owned by users":            {"O:BUD:(A;;FA;;;SY)", false},
		"users may delete":          {"O:SYD:(A;;0x10000;;;BU)", false},
		"users may delete entries":  {"O:SYD:(A;;0x40;;;BU)", false},
		"users may change the dacl": {"O:SYD:(A;;0x40000;;;BU)", false},
		"everyone generic all":      {"O:SYD:(A;;GA;;;WD)", false},
		"object entry":              {"O:SYD:(OA;;FA;bf967aba-0de6-11d0-a285-00aa003049e2;;BU)", false},
		"no dacl":                   {"O:SY", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkTrustedSD(sdFromString(t, tc.sddl), nil)
			if (err == nil) != tc.ok {
				t.Errorf("checkTrustedSD(%s) = %v, want ok = %v", tc.sddl, err, tc.ok)
			}
		})
	}
	// A NULL DACL grants everyone everything. Built by hand: an SDDL parser may
	// turn NO_ACCESS_CONTROL into an empty DACL, which grants nothing.
	t.Run("null dacl", func(t *testing.T) {
		sd, err := windows.NewSecurityDescriptor()
		if err != nil {
			t.Fatal(err)
		}
		system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
		if err != nil {
			t.Fatal(err)
		}
		if err := sd.SetOwner(system, false); err != nil {
			t.Fatal(err)
		}
		if err := sd.SetDACL(nil, true, false); err != nil {
			t.Fatal(err)
		}
		if err := checkTrustedSD(sd, nil); err == nil {
			t.Error("checkTrustedSD accepted a NULL DACL")
		}
	})
}

// An ordinary run trusts its own user with its own temp dir. An elevated run
// trusts nothing unelevated, that same user included — an unelevated process
// of theirs could otherwise swap the directory the root core reads from.
func TestCheckTrustedSD_OnlyTheOrdinaryRunTrustsItsUser(t *testing.T) {
	user := testUserACL(t).user
	sd := sdFromString(t, "O:"+user.String()+"D:(A;OICI;FA;;;"+user.String()+")")

	if err := checkTrustedSD(sd, user); err != nil {
		t.Errorf("ordinary run refused a dir of its own user: %v", err)
	}
	if err := checkTrustedSD(sd, nil); err == nil {
		t.Error("elevated run accepted a dir the unelevated user controls")
	}
}

func TestCheckProtectedSD(t *testing.T) {
	elev, err := elevatedRuntimeACL()
	if err != nil {
		t.Fatal(err)
	}
	user := testUserACL(t)
	for name, tc := range map[string]struct {
		acl  runtimeACL
		sddl string
		ok   bool
	}{
		"elevated as created": {elev, elev.sddl, true},
		"ordinary as created": {user, user.sddl, true},
		"inheriting":          {elev, "O:BAD:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		"owned by system":     {elev, "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		"users may read":      {elev, "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;FR;;;BU)", false},
		"ordinary, users may read": {user, "O:" + user.owner.String() + "D:P(A;OICI;FA;;;" +
			user.owner.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;FR;;;BU)", false},
		"ordinary, owned by another": {user, "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkProtectedSD(sdFromString(t, tc.sddl), tc.acl)
			if (err == nil) != tc.ok {
				t.Errorf("checkProtectedSD(%s) = %v, want ok = %v", tc.sddl, err, tc.ok)
			}
		})
	}
}

// An elevated run's dir is Administrators' from the start, and so is the config
// replaceFile puts into it: it inherits the dir's entries and nothing else.
func TestCreateProtectedDir(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("a dir owned by Administrators needs an elevated run")
	}
	acl, err := elevatedRuntimeACL()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := createProtectedDir(t.TempDir(), acl)
	if err != nil {
		t.Fatalf("createProtectedDir: %v", err)
	}
	config := filepath.Join(dir, "xray_config.json")
	if err := replaceFile(config, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	assertPrivateTo(t, config, acl)
}

// F07: an ordinary run used to take os.MkdirTemp and whatever the temp dir
// handed down. A temp dir that grants everyone read — a machine configuration,
// not a given — must not reach the config with the server's credentials.
func TestClaim_RuntimeDirInheritsNothingFromTheTempDir(t *testing.T) {
	base := isolateRuntime(t)
	open := createDirWithSDDL(t, base, "open", "D:(A;OICI;FA;;;WD)")
	t.Setenv("TMP", open)
	t.Setenv("TEMP", open)

	a := ownClaim(t, t.TempDir())
	if err := a.writeConfigJSON(json.RawMessage(`{"log":{}}`)); err != nil {
		t.Fatalf("writeConfigJSON: %v", err)
	}
	assertRuntimePrivate(t, a.runDir, a.tmpFile)
}

// A base another unprivileged user may rename or re-permission is refused
// outright: whoever can move a directory on the way to the config can put a
// config of their own in its place before the core's next restart. Nothing is
// left behind — not the dir, not the config.
func TestClaim_RefusesUntrustedRuntimeBase(t *testing.T) {
	for name, ace := range map[string]string{
		"others may delete it":       "(A;;0x10000;;;BU)",
		"others may delete entries":  "(A;;0x40;;;BU)",
		"others may change the dacl": "(A;;0x40000;;;BU)",
	} {
		t.Run(name, func(t *testing.T) {
			base := isolateRuntime(t)
			// This is the one test the real check is the subject of.
			checkRuntimeBase = checkTrustedDirs
			// The test's own full access is what lets it make this directory and
			// clean it up afterwards. It is this user's own SID, so it is trusted
			// and changes nothing about what the entry for Users says.
			user := testUserACL(t).user
			loose := createDirWithSDDL(t, base, "loose",
				fmt.Sprintf("O:%[1]sD:(A;OICI;FA;;;%[1]s)(A;OICI;FA;;;SY)%[2]s", user, ace))
			t.Setenv("TMP", loose)
			t.Setenv("TEMP", loose)

			a := newLockApp(t.TempDir())
			err := a.claimInstance()
			if err == nil {
				a.cleanup()
				t.Fatal("claimInstance accepted a base others can change")
			}
			// The refusal names the base the way createRuntimeDir resolved it, and
			// on Windows EvalSymlinks expands an 8.3 short path as well as a link:
			// GitHub's runner hands TMP out as C:\Users\RUNNER~1\…, which the
			// message then spells C:\Users\runneradmin\…. Resolving here compares
			// one directory with itself instead of two spellings of it.
			named := loose
			if resolved, rerr := filepath.EvalSymlinks(loose); rerr == nil {
				named = resolved
			}
			if !strings.Contains(err.Error(), named) {
				t.Errorf("refusal %q does not name the base %q", err, named)
			}
			if a.runDir != "" {
				t.Errorf("runtime dir %q made after the refusal", a.runDir)
			}
			a.cleanup()
			if entries, _ := os.ReadDir(loose); len(entries) != 0 {
				t.Errorf("refused base got %d entries", len(entries))
			}
		})
	}
}

// A temp dir reached through a junction or a symlink is used where it really
// is, so the directories checked are the real ones.
func TestClaim_ResolvesLinkedRuntimeBase(t *testing.T) {
	base := isolateRuntime(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("создание ссылки на каталог недоступно: %v", err)
	}
	// A link the OS accepts but does not resolve is that OS's business, not the
	// runtime dir's.
	if got, err := filepath.EvalSymlinks(link); err != nil || !sameDir(t, got, real) {
		t.Skipf("ссылка на каталог не разрешается в этой среде: %v", err)
	}
	t.Setenv("TMP", link)
	t.Setenv("TEMP", link)

	a := ownClaim(t, t.TempDir())
	if !sameDir(t, filepath.Dir(a.runDir), real) {
		t.Errorf("runtime dir %q, want in the link's target %q", a.runDir, real)
	}
}

// The bases the two runs take must pass on a stock system, or the app would
// refuse to start. This checks the machine, not the code — TestCheckTrustedSD
// has the stock descriptors — so it runs on request: CI images are not stock
// (GitHub's grants Users full control of the system temp, runner-images#1704).
func TestCheckTrustedDirs_Machine(t *testing.T) {
	if os.Getenv("XRAY_RUNNER_MACHINE_CHECKS") != "1" {
		t.Skip("checks this machine's temp dirs; set XRAY_RUNNER_MACHINE_CHECKS=1 to run")
	}
	win, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("elevated base", func(t *testing.T) {
		if err := checkTrustedDirs(filepath.Join(win, "Temp"), nil); err != nil {
			t.Error(err)
		}
	})
	t.Run("ordinary base", func(t *testing.T) {
		base, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := checkTrustedDirs(base, testUserACL(t).user); err != nil {
			t.Error(err)
		}
	})
}
