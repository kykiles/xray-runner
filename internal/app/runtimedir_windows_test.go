//go:build windows

package app

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// isolateRuntime points the runtime base at a fresh directory of the test's, as
// an ordinary run would take it, even from an elevated shell.
func isolateRuntime(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("TMP", base)
	t.Setenv("TEMP", base)
	orig := elevated
	elevated = func() bool { return false }
	t.Cleanup(func() { elevated = orig })
	return base
}

// assertRuntimePrivate has nothing to add for an ordinary run: its dir is in
// the user's own temp, under the ACL inherited from there. An elevated run's
// dir is TestCreateProtectedDir's.
func assertRuntimePrivate(*testing.T, string, string) {}

func sdFromString(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("%s: %v", sddl, err)
	}
	return sd
}

// Whether a directory on the way to an elevated run's config is trusted comes
// down to what an unelevated process may do to it.
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
			err := checkTrustedSD(sdFromString(t, tc.sddl))
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
		if err := checkTrustedSD(sd); err == nil {
			t.Error("checkTrustedSD accepted a NULL DACL")
		}
	})
}

func TestCheckProtectedSD(t *testing.T) {
	for name, tc := range map[string]struct {
		sddl string
		ok   bool
	}{
		"as created":      {runtimeDirSDDL, true},
		"inheriting":      {"O:BAD:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		"owned by system": {"O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		"users may read":  {"O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;FR;;;BU)", false},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkProtectedSD(sdFromString(t, tc.sddl))
			if (err == nil) != tc.ok {
				t.Errorf("checkProtectedSD(%s) = %v, want ok = %v", tc.sddl, err, tc.ok)
			}
		})
	}
}

// An elevated run's dir is Administrators' from the start, and so is the
// config replaceFile puts into it: it inherits the dir's entries and nothing
// else.
func TestCreateProtectedDir(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("a dir owned by Administrators needs an elevated run")
	}
	dir, err := createProtectedDir(t.TempDir())
	if err != nil {
		t.Fatalf("createProtectedDir: %v", err)
	}
	config := filepath.Join(dir, "xray_config.json")
	if err := replaceFile(config, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(config, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	aces, err := daclEntries(sd)
	if err != nil || len(aces) == 0 {
		t.Fatalf("config DACL: %d entries, err %v", len(aces), err)
	}
	for _, e := range aces {
		if e.typ != windows.ACCESS_ALLOWED_ACE_TYPE ||
			!(e.sid.IsWellKnown(windows.WinLocalSystemSid) || e.sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
			t.Errorf("config DACL has an entry of type %d for %v", e.typ, e.sid)
		}
	}
}

// The base an elevated run takes must pass on a stock system, or TUN would
// never start. This checks the machine, not the code — TestCheckTrustedSD has
// the stock descriptor — so it runs on request: CI images are not stock (GitHub's
// grants Users full control of the system temp, runner-images#1704).
func TestCheckTrustedDirs_SystemTemp(t *testing.T) {
	if os.Getenv("XRAY_RUNNER_MACHINE_CHECKS") != "1" {
		t.Skip("checks this machine's system temp; set XRAY_RUNNER_MACHINE_CHECKS=1 to run")
	}
	win, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTrustedDirs(filepath.Join(win, "Temp")); err != nil {
		t.Error(err)
	}
}
