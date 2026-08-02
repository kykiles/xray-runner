//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Split tunnelling: the selected processes are moved into a cgroup v2, and an
// nft nat/output chain redirects that cgroup's traffic into xray's dokodemo-door
// listeners. Everything outside the cgroup — SSH, the rest of the system — keeps
// its normal path, which is the whole point of the mode.
//
// The cgroup match is what makes this work on already-running processes: a PID
// can be moved into a cgroup at any time, unlike an environment variable.

const splitTable = "xray_split"

// Overridable in tests: the real paths need root and a live nft.
var (
	procRoot              = "/proc"
	cgroupRoot            = "/sys/fs/cgroup"
	splitCgroup           = splitCgroupPath()
	nftCmd      commander = execCommander{}
)

// splitCgroupPath picks where the cgroup lives. Root owns the hierarchy root and
// creates the directory there; an unprivileged run — the binary carrying
// CAP_NET_ADMIN via setcap, which grants nft but no file ownership — can only
// write inside the cgroup systemd delegated to the user session.
func splitCgroupPath() string {
	if os.Getuid() == 0 {
		return filepath.Join(cgroupRoot, "xray-split")
	}
	uid := strconv.Itoa(os.Getuid())
	delegated := filepath.Join(cgroupRoot, "user.slice", "user-"+uid+".slice", "user@"+uid+".service")
	if fileExists(delegated) {
		return filepath.Join(delegated, "xray-split")
	}
	// No systemd user manager: the root path is the only candidate left, and
	// failing at MkdirAll with a clear error beats guessing at another layout.
	return filepath.Join(cgroupRoot, "xray-split")
}

// splitLevel is the cgroup's depth below the hierarchy root, which is what nft's
// "socket cgroupv2 level N" compares against: the name alone is not enough, the
// expression has to know which path component to look at. Delegated cgroups sit
// four levels down, the root one at level 1.
func splitLevel() int {
	rel, err := filepath.Rel(cgroupRoot, splitCgroup)
	if err != nil {
		return 1
	}
	return len(strings.Split(filepath.Clean(rel), string(filepath.Separator)))
}

// splitRel is the cgroup as /proc/<pid>/cgroup spells it: rooted at the
// hierarchy, e.g. "/xray-split".
func splitRel() string {
	rel, err := filepath.Rel(cgroupRoot, splitCgroup)
	if err != nil {
		return ""
	}
	return "/" + filepath.Clean(rel)
}

// Destinations that must never be redirected. Loopback and LAN traffic belongs
// to the host — a dev server on 127.0.0.1 or a printer on 192.168.x.x has no
// business crossing the tunnel, and sending it there breaks it outright.
var splitDirectNets = "{ 127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4, 255.255.255.255 }"

// The v6 counterpart: loopback, link-local, unique-local and multicast.
var splitDirectNets6 = "{ ::1/128, fe80::/10, fc00::/7, ff00::/8 }"

// splitHome remembers each moved PID's original cgroup so teardown can put it
// back, mirroring how route_linux.go remembers what it added. The mutex is for
// the rescan: it runs on the health-check goroutine while teardown may be
// emptying the map from the session's.
var (
	splitMu   sync.Mutex
	splitHome = map[string]string{}
)

// currentCgroup reads a PID's cgroup v2 path, e.g. "/user.slice/…/app.scope"
// from the "0::" line of /proc/<pid>/cgroup. Empty means v1-only or unreadable.
func currentCgroup(pid string) string {
	data, err := os.ReadFile(filepath.Join(procRoot, pid, "cgroup"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writePID moves one process into a cgroup. cgroup.procs takes a single pid per
// write and appends it, so this opens for append rather than truncating: the
// kernel ignores O_TRUNC here, but nothing else should be written to assume it.
func writePID(path, pid string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(pid + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ListProcesses returns the distinct user-process names currently running,
// sorted by name. Kernel threads are left out: they have no executable and
// cannot be routed anywhere.
func ListProcesses() ([]Process, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		name := procName(e.Name())
		if name == "" {
			continue
		}
		counts[name]++
	}

	out := make([]Process, 0, len(counts))
	for name, n := range counts {
		out = append(out, Process{Name: name, PIDs: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// procName is the process name for a pid, or "" for a kernel thread. The exe
// link is what tells them apart, and its basename is usually the better name of
// the two: /proc/<pid>/comm is truncated at 15 characters.
//
// Usually, not always. Self-updating tools install each release under its own
// name — claude's binary is …/versions/2.1.220 — so the exe basename is a
// version string that changes under the user's feet and silently stops matching
// apps.txt. comm is "claude" there and stays that way, so it wins whenever it
// is not simply a truncation of the exe basename ("telegram-deskto" is, and
// loses to the full "telegram-desktop").
func procName(pid string) string {
	exe, err := os.Readlink(filepath.Join(procRoot, pid, "exe"))
	if err != nil {
		return ""
	}
	name := filepath.Base(exe)
	if name == "" || name == "." || name == "/" {
		return ""
	}
	// A replaced binary leaves " (deleted)" on the link.
	name = strings.TrimSuffix(name, " (deleted)")

	comm, err := os.ReadFile(filepath.Join(procRoot, pid, "comm"))
	if c := strings.TrimSpace(string(comm)); err == nil && c != "" && !strings.HasPrefix(name, c) {
		return c
	}
	return name
}

// EnableSplit installs the redirect for the named processes and returns the
// names it actually found running. Names that are not running are skipped
// silently — the list is meant to be a standing preference, not an assertion
// about what is up right now.
//
// The rules are installed even when nothing matched, so the caller can move
// late-starting processes in without rebuilding the ruleset.
func EnableSplit(names []string, tcpPort, dnsPort int) ([]string, error) {
	if len(names) == 0 {
		// A run killed before its teardown (SIGKILL, power loss) leaves the table
		// and a populated cgroup behind, and those processes keep redirecting
		// into a port nobody listens on. This is the only sweep that runs on
		// every start, so an emptied apps.txt has to do the cleaning.
		_ = DisableSplit()
		return nil, nil
	}
	if err := nftCmd.lookPath("nft"); err != nil {
		return nil, fmt.Errorf("nftables не найден: %w", err)
	}
	if err := os.MkdirAll(splitCgroup, 0750); err != nil {
		return nil, fmt.Errorf("создать cgroup %s: %w", splitCgroup, err)
	}

	// Rules before processes: a PID that joins the cgroup while the ruleset is
	// still missing sends everything it opens in that window out untunnelled.
	if err := installSplitRules(tcpPort, dnsPort); err != nil {
		// Leave nothing half-built: the cgroup without rules would silently
		// route nothing while the UI claims the processes are tunnelled.
		_ = DisableSplit()
		return nil, err
	}

	matched, err := moveIntoSplit(names)
	if err != nil {
		_ = DisableSplit()
		return nil, err
	}
	return matched, nil
}

// installSplitRules writes the nat/output chain. The table is torn down first so
// a leftover ruleset from a crashed run cannot stack duplicate rules.
func installSplitRules(tcpPort, dnsPort int) error {
	_, _ = nftCmd.run("nft", "delete", "table", "ip", splitTable)
	_, _ = nftCmd.run("nft", "delete", "table", "ip6", splitTable)

	// The socket expression is the cgroup v2 match; meta cgroup is the v1
	// net_cls classid and does not see this hierarchy.
	match := []string{"socket", "cgroupv2", "level", strconv.Itoa(splitLevel()), `"` + filepath.Base(splitCgroup) + `"`}
	rule := func(family, chain string, tail ...string) []string {
		return append(append([]string{"add", "rule", family, splitTable, chain}, match...), tail...)
	}

	cmds := [][]string{
		{"add", "table", "ip", splitTable},
		{"add", "chain", "ip", splitTable, "output", "{ type nat hook output priority -100; policy accept; }"},
		// DNS goes first, before the bypass: the host's resolver usually *is* a
		// bypassed address (127.0.0.53 for systemd-resolved, the router on a LAN
		// address), so a later rule would never see the query.
		rule("ip", "output", "udp", "dport", "53", "redirect", "to", ":"+strconv.Itoa(dnsPort)),
		rule("ip", "output", "ip", "daddr", splitDirectNets, "return"),
		rule("ip", "output", "meta", "l4proto", "tcp", "redirect", "to", ":"+strconv.Itoa(tcpPort)),

		// Only TCP is redirected above, so every other UDP flow — QUIC, a voice
		// call, WebRTC — would walk straight past the tunnel and out of the real
		// interface with the real address. That is a leak, not a missing feature,
		// so it is rejected: QUIC falls back to TCP 443, and the rest fails
		// loudly instead of quietly going direct.
		//
		// A filter chain, not the nat one: nat only sees the first packet of a
		// flow, so a drop there is a side effect of conntrack rather than a rule
		// that plainly holds. Reject over drop for the same reason of clarity —
		// the fallback is immediate instead of waiting out a timeout.
		//
		// The bypass comes first here too: by this hook the redirected DNS is
		// already addressed to 127.0.0.1, and LAN/multicast UDP (mDNS, DHCP,
		// a printer) belongs to the host exactly as it does in the nat chain.
		//
		// ponytail: UDP наружу закрыт целиком; звонки в Telegram перестанут
		// работать, пока процесс в списке. Полноценный UDP через туннель — это
		// TPROXY-инбаунд вместо NAT REDIRECT.
		{"add", "chain", "ip", splitTable, "block", "{ type filter hook output priority 0; policy accept; }"},
		rule("ip", "block", "ip", "daddr", splitDirectNets, "return"),
		rule("ip", "block", "meta", "l4proto", "udp", "reject", "with", "icmp", "type", "port-unreachable"),

		// Соединения, открытые до включения режима, nat уже не увидит: hook
		// output срабатывает на первом пакете потока, а эти потоки начались
		// раньше. Приложение с длинными keep-alive (Claude Code держит HTTP/2 к
		// api.anthropic.com часами) продолжает ходить с реальным адресом, пока
		// не переоткроет сокет — то есть режим включён, а трафик утекает.
		//
		// Здесь остаются только НЕ отредиреченные TCP-потоки: у всего, что nat
		// развернул, daddr уже 127.0.0.1, и его забрал bypass выше. Сброс с RST
		// заставляет приложение переподключиться сразу, и новый поток уже
		// проходит через redirect.
		rule("ip", "block", "meta", "l4proto", "tcp", "reject", "with", "tcp", "reset"),

		// IPv6 is not redirected at all: the nat chain above is the ip family and
		// the dokodemo listener is v4-only. Left alone, an app on a network with
		// IPv6 simply prefers the AAAA record and every byte leaves untunnelled —
		// the worst leak of the mode, because nothing in the UI hints at it.
		// Rejecting it turns Happy Eyeballs around in milliseconds onto the v4
		// path, which is redirected.
		{"add", "table", "ip6", splitTable},
		{"add", "chain", "ip6", splitTable, "block", "{ type filter hook output priority 0; policy accept; }"},
		rule("ip6", "block", "ip6", "daddr", splitDirectNets6, "return"),
		rule("ip6", "block", "reject", "with", "icmpv6", "type", "admin-prohibited"),
	}
	for _, args := range cmds {
		if out, err := nftCmd.run("nft", args...); err != nil {
			return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// RefreshSplit re-scans for processes from the list that are not in the cgroup
// yet and moves them in, returning everything the mode now covers. The nft rules
// match the cgroup, not a PID, so a late-starting app needs nothing else — this
// is what makes "start the app after connecting" work without a reconnect.
//
// It is a no-op when the split tunnel is not up: no cgroup, nothing to join.
func RefreshSplit(names []string) ([]string, error) {
	if len(names) == 0 || !fileExists(splitCgroup) {
		return nil, nil
	}
	return moveIntoSplit(names)
}

// moveIntoSplit puts every PID whose name is in the list into the split cgroup.
func moveIntoSplit(names []string) ([]string, error) {
	splitMu.Lock()
	defer splitMu.Unlock()

	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}

	procs := filepath.Join(splitCgroup, "cgroup.procs")
	found := map[string]bool{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		name := procName(e.Name())
		if name == "" || !want[strings.ToLower(name)] {
			continue
		}
		// Remember where the process lived before teardown puts it back: under
		// systemd every process sits in a slice that carries its resource limits,
		// and dropping it into the root cgroup would quietly lose them.
		home := currentCgroup(e.Name())
		// A process that exited between the scan and the write, or a thread the
		// kernel refuses to move, must not abort the rest of the list.
		if err := writePID(procs, e.Name()); err != nil {
			slog.Debug("split: pid not moved", "pid", e.Name(), "name", name, "error", err)
			continue
		}
		// A rescan re-reads processes it moved itself, whose "home" now *is* the
		// split cgroup: recording that would send them nowhere on teardown.
		if home != "" && home != splitRel() {
			splitHome[e.Name()] = home
		}
		found[name] = true
	}

	out := make([]string, 0, len(found))
	for n := range found {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// DisableSplit removes the ruleset and empties the cgroup. It is safe to call
// when nothing was ever enabled, which is what every teardown path does.
func DisableSplit() error {
	splitMu.Lock()
	defer splitMu.Unlock()

	// A missing table is the expected case on a clean run, not an error.
	_, _ = nftCmd.run("nft", "delete", "table", "ip", splitTable)
	_, _ = nftCmd.run("nft", "delete", "table", "ip6", splitTable)

	if _, err := os.Stat(splitCgroup); err != nil {
		return nil
	}
	// The cgroup has to be emptied before it can be removed: rmdir refuses a
	// populated one. Each process goes back where it came from, falling back to
	// the root cgroup when its old one is gone (a closed systemd scope).
	data, err := os.ReadFile(filepath.Join(splitCgroup, "cgroup.procs"))
	if err == nil {
		for pid := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			if pid == "" {
				continue
			}
			dest := filepath.Join(cgroupRoot, "cgroup.procs")
			if home := splitHome[pid]; home != "" {
				if target := filepath.Join(cgroupRoot, home, "cgroup.procs"); fileExists(target) {
					dest = target
				}
			}
			if err := writePID(dest, pid); err != nil {
				slog.Debug("split: pid not restored", "pid", pid, "dest", dest, "error", err)
			}
		}
	}
	clear(splitHome)
	// RemoveAll, not Remove: on cgroupfs the directory goes in one rmdir once it
	// is empty of processes, and the fallback recursion is what makes the same
	// call work on an ordinary directory under test.
	if err := os.RemoveAll(splitCgroup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("удалить cgroup %s: %w", splitCgroup, err)
	}
	return nil
}
