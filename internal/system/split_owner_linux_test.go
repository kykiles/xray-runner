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

// For one user the scan does not close anything itself — the service cannot
// tell whose sockets are whose — but names the processes it moved in, so the
// user can list their connections and have them closed.
func TestEnableSplitForHandsMovedToCaller(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code"})
	writeStatus(t, "42", "1000\t1000\t1000\t1000")
	scan, err := EnableSplitFor([]string{"code"}, 10810, 10853, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Moved["42"] != "code" || len(scan.Unclosed) != 0 {
		t.Fatalf("scan %+v", scan)
	}
	for _, c := range stub.calls {
		if len(c) > 1 && c[0] == "ss" {
			t.Fatalf("ss run by the service's scan: %v", c)
		}
	}
	// A rescan that finds it already moved hands nothing over again.
	if err := os.WriteFile(filepath.Join(procRoot, "42", "cgroup"), []byte("0::"+splitRel()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err = RefreshSplitFor([]string{"code"}, 1000)
	if err != nil || len(scan.Moved) != 0 {
		t.Fatalf("rescan %+v %v", scan, err)
	}
}

// Only the user's own sockets are closed, and only those the split would
// have redirected; the rest are reported open and left alone.
func TestCloseConnsChecksOwner(t *testing.T) {
	stub := withFakes(t, nil)
	stub.killed = true
	mine := Conn{Src: "192.168.1.5:40000", Dst: "160.79.104.10:443"}
	lan := Conn{Src: "192.168.1.5:40001", Dst: "192.168.1.1:443"}
	bad := Conn{Src: "x; rm", Dst: "160.79.104.10:443"}

	stub.sockUID = "1000"
	if open := CloseConns([]Conn{mine, lan, bad}, 1000); len(open) != 2 || open[0] != lan || open[1] != bad {
		t.Fatalf("open %v", open)
	}
	kills := 0
	for _, c := range stub.calls {
		if c[0] == "ss" && c[1] == "-K" {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("%d kills, want 1", kills)
	}

	stub.sockUID = "1001"
	if open := CloseConns([]Conn{mine}, 1000); len(open) != 1 {
		t.Fatal("another user's connection closed")
	}
	stub.sockUID = ""
	if open := CloseConns([]Conn{mine}, 1000); len(open) != 1 {
		t.Fatal("root's connection closed for a user")
	}
	if open := CloseConns([]Conn{mine}, 0); len(open) != 0 {
		t.Fatal("root's own connection not closed")
	}
}

func TestConnsOf(t *testing.T) {
	stub := withFakes(t, nil)
	stub.ssOut = `0 0 192.168.1.5:40000 160.79.104.10:443 users:(("code",pid=42,fd=20),("code",pid=43,fd=20))
0 0 127.0.0.1:5000 127.0.0.1:6000 users:(("code",pid=42,fd=21))
0 0 192.168.1.5:40002 1.1.1.1:443 users:(("sshd",pid=99,fd=3))`
	conns, err := ConnsOf(map[string]bool{"42": true})
	if err != nil {
		t.Fatal(err)
	}
	want := Conn{Src: "192.168.1.5:40000", Dst: "160.79.104.10:443"}
	if len(conns) != 1 || len(conns[want]) != 1 || conns[want][0] != "42" {
		t.Fatalf("conns %v", conns)
	}
}
