//go:build linux

package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc builds a /proc-shaped tree: pid → executable path. A pid with an
// empty exe is a kernel thread and must be invisible to both the picker and the
// mover.
func fakeProc(t *testing.T, procs map[string]string) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for pid, exe := range procs {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if exe == "" {
			continue
		}
		target := filepath.Join(bin, exe)
		if err := os.WriteFile(target, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
		cgroup := "0::/user.slice/app-" + exe + ".scope\n"
		if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A non-numeric entry: /proc is full of them and none is a process.
	if err := os.MkdirAll(filepath.Join(root, "self"), 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

// stubNft records every nft invocation instead of touching the kernel.
type stubNft struct {
	calls   [][]string
	failOn  string
	missing bool
}

func (s *stubNft) lookPath(string) error {
	if s.missing {
		return os.ErrNotExist
	}
	return nil
}

func (s *stubNft) run(bin string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{bin}, args...))
	if s.failOn != "" && strings.Contains(strings.Join(args, " "), s.failOn) {
		return []byte("boom"), os.ErrPermission
	}
	return nil, nil
}

// withFakes points the package at a temp /proc and cgroup tree.
func withFakes(t *testing.T, procs map[string]string) *stubNft {
	t.Helper()
	stub := &stubNft{}
	cg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cg, "cgroup.procs"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	oldProc, oldRoot, oldSplit, oldCmd := procRoot, cgroupRoot, splitCgroup, nftCmd
	procRoot, cgroupRoot, splitCgroup, nftCmd = fakeProc(t, procs), cg, filepath.Join(cg, "xray-split"), stub
	clear(splitHome)
	t.Cleanup(func() {
		procRoot, cgroupRoot, splitCgroup, nftCmd = oldProc, oldRoot, oldSplit, oldCmd
		clear(splitHome)
	})
	return stub
}

func TestListProcessesSkipsKernelThreads(t *testing.T) {
	withFakes(t, map[string]string{"1": "systemd", "2": "", "42": "code", "43": "code"})

	got, err := ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	want := []Process{{Name: "code", PIDs: 2}, {Name: "systemd", PIDs: 1}}
	if len(got) != len(want) {
		t.Fatalf("ListProcesses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A name in the list that is not running is skipped, not an error: the file is
// a standing preference, and the task explicitly asks for silence here.
func TestEnableSplitSkipsMissingProcesses(t *testing.T) {
	withFakes(t, map[string]string{"42": "code", "99": "sshd"})

	matched, err := EnableSplit([]string{"code", "telegram-desktop"}, 10810, 10853)
	if err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	if len(matched) != 1 || matched[0] != "code" {
		t.Fatalf("matched = %v, want [code]", matched)
	}

	moved, err := os.ReadFile(filepath.Join(splitCgroup, "cgroup.procs"))
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	if strings.TrimSpace(string(moved)) != "42" {
		t.Errorf("moved pids = %q, want 42 — sshd must stay outside the tunnel", moved)
	}
}

// The rule order is the load-bearing part: DNS has to be redirected before the
// bypass returns, or a resolver on 127.0.0.53 leaks every lookup.
func TestEnableSplitRuleOrder(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code"})

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}

	var rules []string
	for _, c := range stub.calls {
		if line := strings.Join(c, " "); strings.Contains(line, "add rule") {
			rules = append(rules, line)
		}
	}
	if len(rules) != 4 {
		t.Fatalf("got %d rules, want 4: %v", len(rules), rules)
	}
	// The QUIC block comes last and lives in the filter chain, so it does not
	// disturb the nat ordering above.
	for i, want := range []string{
		"udp dport 53 redirect to :10853",
		"daddr",
		"l4proto tcp redirect to :10810",
		"udp dport 443 reject",
	} {
		if !strings.Contains(rules[i], want) {
			t.Errorf("rule[%d] = %q, want it to contain %q", i, rules[i], want)
		}
	}
	for i, r := range rules {
		if !strings.Contains(r, `socket cgroupv2 level 1 "xray-split"`) {
			t.Errorf("rule[%d] = %q, missing the cgroup match — it would capture the whole host", i, r)
		}
	}
}

// An unprivileged run puts the cgroup under the delegated user@<uid>.service,
// four levels down. The nft match has to follow it: "level 1" there names
// user.slice and would capture the whole session instead of the listed apps.
func TestEnableSplitMatchesDelegatedCgroupLevel(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code"})
	splitCgroup = filepath.Join(cgroupRoot, "user.slice", "user-1000.slice", "user@1000.service", "xray-split")

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}

	for _, c := range stub.calls {
		line := strings.Join(c, " ")
		if !strings.Contains(line, "add rule") {
			continue
		}
		if !strings.Contains(line, `socket cgroupv2 level 4 "xray-split"`) {
			t.Errorf("rule = %q, want level 4 for the delegated cgroup", line)
		}
	}
}

// A failed ruleset must not leave the cgroup behind: processes inside it with no
// rules would be reported as tunnelled while routing nowhere.
func TestEnableSplitRollsBackOnRuleFailure(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code"})
	stub.failOn = "add rule"

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err == nil {
		t.Fatal("EnableSplit succeeded despite a failing nft rule")
	}
	if _, err := os.Stat(splitCgroup); !os.IsNotExist(err) {
		t.Errorf("cgroup %s survived a failed bring-up", splitCgroup)
	}
}

// Teardown puts each process back in the cgroup it came from, so a VS Code that
// started under a systemd scope keeps that scope's limits afterwards.
func TestDisableSplitRestoresOriginalCgroup(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})

	home := filepath.Join(cgroupRoot, "user.slice", "app-code.scope")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cgroup.procs"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	if err := DisableSplit(); err != nil {
		t.Fatalf("DisableSplit: %v", err)
	}

	back, err := os.ReadFile(filepath.Join(home, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(back)) != "42" {
		t.Errorf("pid restored to %q, want its original scope", back)
	}
	if _, err := os.Stat(splitCgroup); !os.IsNotExist(err) {
		t.Error("split cgroup not removed")
	}
}

// Every teardown path calls this, including runs that never enabled anything.
func TestDisableSplitIsIdempotent(t *testing.T) {
	withFakes(t, nil)
	for range 2 {
		if err := DisableSplit(); err != nil {
			t.Fatalf("DisableSplit on a clean system: %v", err)
		}
	}
}

func TestLoadAppsSkipsCommentsAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), AppsFile)
	body := "# заголовок\ncode\n\n  telegram-desktop  # мессенджер\nCODE\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadApps(path)
	if err != nil {
		t.Fatalf("LoadApps: %v", err)
	}
	want := []string{"code", "telegram-desktop"}
	if len(got) != len(want) {
		t.Fatalf("LoadApps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A missing file is the default state on a first run, not an error.
func TestLoadAppsMissingFile(t *testing.T) {
	got, err := LoadApps(filepath.Join(t.TempDir(), "nope.txt"))
	if err != nil || got != nil {
		t.Fatalf("LoadApps(missing) = %v, %v; want nil, nil", got, err)
	}
}

func TestSaveAppsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), AppsFile)
	want := []string{"code", "telegram-desktop"}
	if err := SaveApps(path, want); err != nil {
		t.Fatalf("SaveApps: %v", err)
	}
	got, err := LoadApps(path)
	if err != nil {
		t.Fatalf("LoadApps: %v", err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}
