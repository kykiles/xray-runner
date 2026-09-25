//go:build linux

package netcap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// status writes a /proc/self/status with the given permitted set and points the
// package at it, as the uid given.
func status(t *testing.T, euid int, capPrm string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "status")
	body := "Name:\txray-runner\nCapInh:\t0000000000000000\nCapPrm:\t" + capPrm + "\nCapEff:\t0000000000000000\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	origPath, origEuid, origHeld := statusPath, geteuid, held
	statusPath, geteuid = path, func() int { return euid }
	held = sync.OnceValue(func() bool { return permitted(statusPath) })
	t.Cleanup(func() { statusPath, geteuid, held = origPath, origEuid, origHeld })
}

func TestDelegated(t *testing.T) {
	for name, tc := range map[string]struct {
		euid   int
		capPrm string
		want   bool
	}{
		"setcap cap_net_admin+ep": {1000, "0000000000001000", true},
		"more than that":          {1000, "000001ffffffffff", true},
		"no capability":           {1000, "0000000000000000", false},
		"another one only":        {1000, "0000000000002000", false},
		"root":                    {0, "000001ffffffffff", false},
		"unreadable set":          {1000, "zz", false},
	} {
		t.Run(name, func(t *testing.T) {
			status(t, tc.euid, tc.capPrm)
			if got := Delegated(); got != tc.want {
				t.Errorf("Delegated() = %v, want %v", got, tc.want)
			}
			cmd := exec.Command("true")
			Prepare(cmd)
			got := cmd.SysProcAttr != nil && len(cmd.SysProcAttr.AmbientCaps) == 1 && cmd.SysProcAttr.AmbientCaps[0] == capNetAdmin
			if got != tc.want {
				t.Errorf("CAP_NET_ADMIN handed to the child = %v, want %v", got, tc.want)
			}
		})
	}
}

// A Delegated run hands the capability to what it starts, so the name it is
// given resolves only in the directories root owns: a PATH the caller set
// cannot point ip at a program of its own.
func TestLookPath_DelegatedIgnoresPATH(t *testing.T) {
	planted := t.TempDir()
	if err := os.WriteFile(filepath.Join(planted, "xray-runner-test-tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", planted+string(os.PathListSeparator)+os.Getenv("PATH"))

	status(t, 1000, "0000000000000000")
	if p, err := LookPath("xray-runner-test-tool"); err != nil || filepath.Dir(p) != planted {
		t.Errorf("without the capability: LookPath = %q, %v; want the one on PATH", p, err)
	}

	status(t, 1000, "0000000000001000")
	p, err := LookPath("xray-runner-test-tool")
	if err == nil {
		t.Fatalf("with the capability: LookPath found %q on the caller's PATH", p)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want one that reads as exec.ErrNotFound", err)
	}
	sh, err := LookPath("sh")
	if err != nil {
		t.Fatalf("LookPath(sh): %v", err)
	}
	if dir := filepath.Dir(sh); dir != "/usr/local/sbin" && dir != "/usr/local/bin" && dir != "/usr/sbin" && dir != "/usr/bin" && dir != "/sbin" && dir != "/bin" {
		t.Errorf("sh found in %s, outside the system directories", dir)
	}
}
