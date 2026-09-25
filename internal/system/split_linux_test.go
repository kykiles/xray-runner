//go:build linux

package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// stubNft records every ss invocation instead of touching the host's sockets.
type stubNft struct {
	calls   [][]string
	failOn  string
	missing bool
	ssOut   string // what "ss -tnHp" reports
	noSS    bool   // ss is not installed
	killOut string // what "ss -K" prints
	killed  bool   // "ss -K" closes the connection it is given
	gone    bool   // the connection ends on its own before "ss -K" gets to it
}

func (s *stubNft) lookPath(string) error {
	if s.missing {
		return os.ErrNotExist
	}
	return nil
}

func (s *stubNft) run(bin string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{bin}, args...))
	if bin == "ss" && s.noSS {
		return nil, &exec.Error{Name: "ss", Err: exec.ErrNotFound}
	}
	if s.failOn != "" && strings.Contains(strings.Join(args, " "), s.failOn) {
		return []byte("boom"), os.ErrPermission
	}
	if bin == "ss" && len(args) > 0 && args[0] == "-tnHp" {
		return []byte(s.ssOut), nil
	}
	if bin == "ss" && len(args) > 0 && args[0] == "-K" {
		// Like the real one, ss -K exits 0 whether it closed anything or not.
		return []byte(s.killOut), nil
	}
	if bin == "ss" && len(args) > 0 && args[0] == "-tnH" {
		// Is the connection from src to dst still up?
		if s.killed || s.gone {
			return nil, nil
		}
		src, dst := args[slices.Index(args, "src")+1], args[slices.Index(args, "dst")+1]
		return []byte("0 0 " + src + " " + dst + "\n"), nil
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

	withFakeNft(t)
	oldProc, oldRoot, oldSplit, oldCmd := procRoot, cgroupRoot, splitCgroup, ssCmd
	procRoot, cgroupRoot, splitCgroup, ssCmd = fakeProc(t, procs), cg, filepath.Join(cg, "xray-split"), stub
	clear(splitHome)
	clear(splitUnclosed)
	t.Cleanup(func() {
		procRoot, cgroupRoot, splitCgroup, ssCmd = oldProc, oldRoot, oldSplit, oldCmd
		clear(splitHome)
		clear(splitUnclosed)
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

// A self-updating tool installs each release under its own name (claude lives at
// .../versions/2.1.220), so the exe basename is a version string that goes stale
// on the next update. comm holds the real name and must win — but only when it is
// not just the 15-character truncation of a longer exe name.
func TestProcNameFallsBackToComm(t *testing.T) {
	withFakes(t, map[string]string{"10": "2.1.220", "11": "telegram-desktop"})
	for pid, comm := range map[string]string{"10": "claude", "11": "telegram-deskto"} {
		if err := os.WriteFile(filepath.Join(procRoot, pid, "comm"), []byte(comm+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	want := []Process{{Name: "claude", PIDs: 1}, {Name: "telegram-desktop", PIDs: 1}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// RefreshSplit is what makes an app started after connecting join the tunnel: it
// moves the newcomer in and leaves the earlier PID's original cgroup recorded,
// so teardown still puts it back where it came from.
func TestRefreshSplitPicksUpLateProcesses(t *testing.T) {
	withFakes(t, map[string]string{"10": "code"})
	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	home := splitHome["10"]

	// The second process shows up only now, mid-session. The first one already
	// reports the split cgroup as its own — that is what the rescan must not
	// record as its home.
	procRoot = fakeProc(t, map[string]string{"10": "code", "11": "code"})
	if err := os.WriteFile(filepath.Join(procRoot, "10", "cgroup"), []byte("0::"+splitRel()+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	scan, err := RefreshSplit([]string{"code"})
	if err != nil {
		t.Fatalf("RefreshSplit: %v", err)
	}
	if matched := scan.Matched; len(matched) != 1 || matched[0] != "code" {
		t.Errorf("matched = %v, want [code]", matched)
	}
	procs, err := os.ReadFile(filepath.Join(splitCgroup, "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(procs), "11") {
		t.Errorf("cgroup.procs = %q, want the late pid 11 in it", procs)
	}
	if splitHome["10"] != home {
		t.Errorf("splitHome[10] = %q, want the pre-move cgroup %q", splitHome["10"], home)
	}
}

// Сокеты, открытые до переезда процесса в cgroup, правила не видят вообще:
// "socket cgroupv2" смотрит на cgroup времени создания сокета. Их надо закрыть
// руками — иначе долгий keep-alive так и ходит мимо туннеля. Закрывать можно
// только внешние адреса и только у тех, кто действительно только что переехал.
func TestEnableSplitClosesPreExistingConnections(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code", "99": "sshd"})
	stub.ssOut = strings.Join([]string{
		`0 0 192.168.31.94:56196 160.79.104.10:443 users:(("code",pid=42,fd=20))`,
		`0 0 127.0.0.1:37442 127.0.0.1:40276 users:(("code",pid=42,fd=62))`,
		`0 0 192.168.31.94:45278 18.97.36.2:443 users:(("sshd",pid=99,fd=18))`,
	}, "\n")

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}

	var killed []string
	for _, c := range stub.calls {
		if c[0] == "ss" && c[1] == "-K" {
			killed = append(killed, strings.Join(c, " "))
		}
	}
	want := "ss -K state established src 192.168.31.94:56196 dst 160.79.104.10:443"
	if len(killed) != 1 || killed[0] != want {
		t.Fatalf("killed = %v, want exactly [%s]", killed, want)
	}

	// Второй проход: процесс уже в cgroup, его новые сокеты идут через redirect.
	// Рвать их каждую секунду — значит не давать приложению вообще работать.
	if err := os.WriteFile(filepath.Join(procRoot, "42", "cgroup"), []byte("0::"+splitRel()+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stub.calls = nil
	if _, err := RefreshSplit([]string{"code"}); err != nil {
		t.Fatalf("RefreshSplit: %v", err)
	}
	for _, c := range stub.calls {
		if c[0] == "ss" && c[1] == "-K" {
			t.Errorf("rescan killed a socket it had already tunnelled: %v", c)
		}
	}
}

// ss -K says nothing through its exit status: a refused kill (no CAP_NET_ADMIN,
// a kernel without SOCK_DESTROY) exits 0 like a successful one. Whatever stopped
// it, an app whose old connection is still up comes back in Unclosed — the move
// itself worked, so the split stays up (14b). A connection that ended by itself
// before the kill is no leak and no reason to warn.
func TestSplitReportsUnclosedConnections(t *testing.T) {
	refused := "SOCK_DESTROY answers: Operation not permitted\nNetid Recv-Q Send-Q Local Address:Port Peer Address:Port\n"
	cases := []struct {
		name  string
		setup func(*stubNft)
		want  []string
	}{
		{"closed", func(s *stubNft) { s.killed = true }, nil},
		{"ended on its own", func(s *stubNft) { s.gone = true }, nil},
		{"nothing to close", func(s *stubNft) {
			s.ssOut = `0 0 127.0.0.1:37442 127.0.0.1:40276 users:(("code",pid=42,fd=62))`
		}, nil},
		{"ss missing", func(s *stubNft) { s.noSS = true }, []string{"code"}},
		{"kill refused", func(s *stubNft) { s.killOut = refused }, []string{"code"}},
		{"kill failed", func(s *stubNft) { s.failOn = "-K" }, []string{"code"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := withFakes(t, map[string]string{"42": "code", "99": "sshd"})
			stub.ssOut = strings.Join([]string{
				`0 0 192.168.31.94:56196 160.79.104.10:443 users:(("code",pid=42,fd=20))`,
				`0 0 192.168.31.94:45278 18.97.36.2:443 users:(("sshd",pid=99,fd=18))`,
			}, "\n")
			c.setup(stub)

			scan, err := EnableSplit([]string{"code"}, 10810, 10853)
			if err != nil {
				t.Fatalf("EnableSplit: %v", err)
			}
			if !slices.Equal(scan.Matched, []string{"code"}) {
				t.Errorf("Matched = %v, want [code]", scan.Matched)
			}
			if !slices.Equal(scan.Unclosed, c.want) {
				t.Errorf("Unclosed = %v, want %v", scan.Unclosed, c.want)
			}
		})
	}
}

// The rescan does not retry a failed kill — by then the app's new sockets are
// tunnelled, and a kill would hit those — but it must not forget the failure
// either: the app stays in Unclosed until it exits, so no scan reads as "all of
// it tunnelled" while the old connection is still out there (14b).
func TestSplitRescanKeepsUnclosedUntilExit(t *testing.T) {
	stub := withFakes(t, map[string]string{"42": "code"})
	stub.ssOut = `0 0 192.168.31.94:56196 160.79.104.10:443 users:(("code",pid=42,fd=20))`
	stub.failOn = "-K"

	scan, err := EnableSplit([]string{"code"}, 10810, 10853)
	if err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	if !slices.Equal(scan.Unclosed, []string{"code"}) {
		t.Fatalf("Unclosed = %v, want [code]", scan.Unclosed)
	}

	// Next tick: pid 42 is in the cgroup now.
	if err := os.WriteFile(filepath.Join(procRoot, "42", "cgroup"), []byte("0::"+splitRel()+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stub.calls = nil
	if scan, err = RefreshSplit([]string{"code"}); err != nil {
		t.Fatalf("RefreshSplit: %v", err)
	}
	if !slices.Equal(scan.Unclosed, []string{"code"}) {
		t.Errorf("rescan Unclosed = %v, want [code]: the failed kill was forgotten", scan.Unclosed)
	}
	for _, c := range stub.calls {
		if c[0] == "ss" && c[1] == "-K" {
			t.Errorf("rescan retried the kill on a process already in the tunnel: %v", c)
		}
	}

	// The app restarted: pid 42 is gone, the new one comes in from outside, and
	// its old connection closes.
	procRoot = fakeProc(t, map[string]string{"43": "code"})
	stub.ssOut = `0 0 192.168.31.94:56200 160.79.104.10:443 users:(("code",pid=43,fd=20))`
	stub.failOn, stub.killed = "", true
	if scan, err = RefreshSplit([]string{"code"}); err != nil {
		t.Fatalf("RefreshSplit: %v", err)
	}
	if !slices.Equal(scan.Matched, []string{"code"}) || len(scan.Unclosed) != 0 {
		t.Errorf("after the restart = %+v, want code matched and nothing unclosed", scan)
	}
}

// A name in the list that is not running is skipped, not an error: the file is
// a standing preference, and the task explicitly asks for silence here.
func TestEnableSplitSkipsMissingProcesses(t *testing.T) {
	withFakes(t, map[string]string{"42": "code", "99": "sshd"})

	scan, err := EnableSplit([]string{"code", "telegram-desktop"}, 10810, 10853)
	if err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	if matched := scan.Matched; len(matched) != 1 || matched[0] != "code" {
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

// What the split does to a packet, by the order of its rules: DNS is
// redirected before the bypass returns, or a resolver on 127.0.0.53 leaks every
// lookup; the LAN keeps its path; everything the redirect cannot carry — other
// UDP, all of IPv6 — is rejected rather than left to go out directly; and none
// of it reaches a process outside the cgroup.
func TestSplitDecisions(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	f := nftOps.(*fakeNft)
	nat, block := f.table[splitNatChain], f.table[splitBlockChain]
	if !nat.nat || nat.priority != -100 || block.nat || block.priority != 0 {
		t.Fatalf("chains %+v / %+v: want nat at -100 and filter at 0", nat, block)
	}

	const in = "xray-split"
	cases := []struct {
		name  string
		p     packet
		nat   nftRule // the nat chain's verdict
		block nftVerdict
	}{
		{"DNS to the local resolver", packet{cgroup: in, daddr: "127.0.0.53", l4proto: protoUDP, dport: 53},
			nftRule{verdict: nftRedirect, port: 10853}, nftReturn},
		{"TCP out", packet{cgroup: in, daddr: "1.1.1.1", l4proto: protoTCP, dport: 443},
			nftRule{verdict: nftRedirect, port: 10810}, nftReturn},
		{"LAN", packet{cgroup: in, daddr: "192.168.1.10", l4proto: protoTCP, dport: 445},
			nftRule{verdict: nftReturn}, nftReturn},
		{"QUIC", packet{cgroup: in, daddr: "1.1.1.1", l4proto: protoUDP, dport: 443},
			nftRule{verdict: nftAccept}, nftRejectICMP},
		{"IPv6", packet{cgroup: in, daddr: "2606:4700::1111", l4proto: protoTCP, dport: 443},
			nftRule{verdict: nftAccept}, nftRejectICMP},
		{"IPv6 link-local", packet{cgroup: in, daddr: "fe80::1", l4proto: protoUDP, dport: 5353},
			nftRule{verdict: nftAccept}, nftReturn},
		{"outside the cgroup", packet{cgroup: "user.slice", daddr: "1.1.1.1", l4proto: protoTCP, dport: 443},
			nftRule{verdict: nftAccept}, nftAccept},
	}
	for _, c := range cases {
		gotNat, _ := nftEval(nat, c.p)
		if gotNat.verdict != c.nat.verdict || gotNat.port != c.nat.port {
			t.Errorf("%s: nat %v port %d, want %v port %d", c.name, gotNat.verdict, gotNat.port, c.nat.verdict, c.nat.port)
		}
		// By the filter hook a redirected packet is addressed to loopback, and
		// the bypass returns it.
		p := c.p
		if gotNat.verdict == nftRedirect {
			p.daddr = "127.0.0.1"
		}
		if gotBlock, _ := nftEval(block, p); gotBlock.verdict != c.block {
			t.Errorf("%s: filter %v, want %v", c.name, gotBlock.verdict, c.block)
		}
	}
	// TCP that nat did not turn around — it still carries its real address —
	// is reset rather than let out.
	if r, _ := nftEval(block, packet{cgroup: in, daddr: "1.1.1.1", l4proto: protoTCP, dport: 443}); r.verdict != nftRejectTCP {
		t.Errorf("unredirected TCP: %v, want a reset", r.verdict)
	}
	for _, ch := range []nftChain{nat, block} {
		for i, r := range ch.rules {
			if r.cgroup != in || r.level != 1 {
				t.Errorf("%s rule %d = %+v, missing the cgroup match — it would capture the whole host", ch.name, i, r)
			}
		}
	}
}

// An empty list is also the sweep for a run that was killed before its
// teardown: its chains would otherwise keep redirecting the leftover cgroup
// into a port nobody listens on.
func TestEnableSplitWithNoNamesClearsStaleRules(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	f := nftOps.(*fakeNft)
	f.table = map[string]nftChain{splitNatChain: {name: splitNatChain}, splitBlockChain: {name: splitBlockChain}}

	if _, err := EnableSplit(nil, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	if f.table != nil {
		t.Errorf("stale chains left: %v", f.table)
	}
}

// An unprivileged run puts the cgroup under the delegated user@<uid>.service,
// four levels down. The match has to follow it: level 1 there names
// user.slice and would capture the whole session instead of the listed apps.
// And the path is spelled from the hierarchy root: the last component alone
// names no cgroup at all (G05).
func TestEnableSplitMatchesDelegatedCgroupLevel(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	splitCgroup = filepath.Join(cgroupRoot, "user.slice", "user-1000.slice", "user@1000.service", "xray-split")

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	f := nftOps.(*fakeNft)
	for _, ch := range f.table {
		for _, r := range ch.rules {
			if r.level != 4 || r.cgroup != "user.slice/user-1000.slice/user@1000.service/xray-split" {
				t.Errorf("rule %+v, want level 4 for the delegated cgroup", r)
			}
		}
	}
}

// A failed ruleset must not leave the cgroup behind: processes inside it with no
// rules would be reported as tunnelled while routing nowhere.
func TestEnableSplitRollsBackOnRuleFailure(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	nftOps.(*fakeNft).failApply = syscall.EOPNOTSUPP

	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err == nil {
		t.Fatal("EnableSplit succeeded despite a refused batch")
	}
	if _, err := os.Stat(splitCgroup); !os.IsNotExist(err) {
		t.Errorf("cgroup %s survived a failed bring-up", splitCgroup)
	}
}

// The split leaves the kill switch's chain alone, and the table with it.
func TestDisableSplitKeepsOtherChains(t *testing.T) {
	withFakes(t, map[string]string{"42": "code"})
	if _, err := EnableSplit([]string{"code"}, 10810, 10853); err != nil {
		t.Fatalf("EnableSplit: %v", err)
	}
	f := nftOps.(*fakeNft)
	f.table[killSwitchChain] = nftChain{name: killSwitchChain}
	if err := DisableSplit(); err != nil {
		t.Fatal(err)
	}
	if chains, _ := f.chains(); !slices.Equal(chains, []string{killSwitchChain}) {
		t.Errorf("chains after the split went: %v", chains)
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
