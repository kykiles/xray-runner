//go:build linux

package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStatus(t *testing.T, pid, uids string) {
	t.Helper()
	status := "Name:\tx\nUid:\t" + uids + "\nGid:\t0\t0\t0\t0\n"
	if err := os.WriteFile(filepath.Join(procRoot, pid, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The service moves only the asking user's processes (H10): a process of the
// same name run by somebody else, or one the user started setuid, stays put.
func TestEnableSplitForMovesOnlyOwnProcesses(t *testing.T) {
	withFakes(t, map[string]string{"42": "code", "43": "code", "44": "code", "45": "code"})
	writeStatus(t, "42", "1000\t1000\t1000\t1000")
	writeStatus(t, "43", "1001\t1001\t1001\t1001")
	writeStatus(t, "44", "1000\t0\t0\t0")
	// 45 has no status: gone, or not readable — not the user's.

	scan, err := EnableSplitFor([]string{"code"}, 10810, 10853, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Matched) != 1 {
		t.Fatalf("matched = %v", scan.Matched)
	}
	moved, err := os.ReadFile(filepath.Join(splitCgroup, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(moved)); len(got) != 1 || got[0] != "42" {
		t.Fatalf("moved %v, want only 42", got)
	}
}

// Without a filter everybody's processes move, as under sudo.
func TestEnableSplitForAnyone(t *testing.T) {
	withFakes(t, map[string]string{"42": "code", "43": "code"})
	if _, err := EnableSplitFor([]string{"code"}, 10810, 10853, -1); err != nil {
		t.Fatal(err)
	}
	moved, _ := os.ReadFile(filepath.Join(splitCgroup, "cgroup.procs"))
	if len(strings.Fields(string(moved))) != 2 {
		t.Fatalf("moved %q", moved)
	}
}

// A name cut to comm's 15 characters — all the service sees of a process
// whose exe link it may not follow — still matches the listed name, and is
// reported by the list's spelling.
func TestListedName(t *testing.T) {
	want := map[string]string{"telegram-desktop": "Telegram-Desktop", "curl": "curl"}
	cases := map[string]string{
		"telegram-deskto":  "Telegram-Desktop",
		"TELEGRAM-DESKTOP": "Telegram-Desktop",
		"curl":             "curl",
		"cur":              "",
		"telegram":         "",
		"":                 "",
	}
	for in, out := range cases {
		got, ok := listedName(in, want)
		if got != out || ok != (out != "") {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, out)
		}
	}
}
