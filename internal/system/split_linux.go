//go:build linux

package system

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Split tunnelling: the selected processes are moved into a cgroup v2, and a
// nat/output chain in our nftables table (nft_linux.go) redirects that
// cgroup's traffic into xray's dokodemo-door listeners. Everything outside the cgroup — SSH, the rest of the system — keeps
// its normal path, which is the whole point of the mode.
//
// The cgroup match is what makes this work on already-running processes: a PID
// can be moved into a cgroup at any time, unlike an environment variable.

// The split's chains in our table.
const (
	splitNatChain   = "split_nat"
	splitBlockChain = "split_block"
)

var splitChains = []string{splitNatChain, splitBlockChain}

// SplitOverTUN says how per-process routing is built here: on Linux it is the
// cgroup + nft redirect below, riding on proxy mode rather than on the tunnel.
const SplitOverTUN = false

// Overridable in tests: the real paths need root, and ss a live host.
var (
	procRoot              = "/proc"
	cgroupRoot            = "/sys/fs/cgroup"
	splitCgroup           = splitCgroupPath()
	ssCmd       commander = execCommander{}
)

// splitCgroupPath picks where the cgroup lives. Root owns the hierarchy root and
// creates the directory there; an unprivileged run — the binary carrying
// CAP_NET_ADMIN via setcap, which grants netfilter but no file ownership — can only
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
var splitDirectNets = prefixes("127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32")

// The v6 counterpart: loopback, link-local, unique-local and multicast.
var splitDirectNets6 = prefixes("::1/128", "fe80::/10", "fc00::/7", "ff00::/8")

func prefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// splitHome remembers each moved PID's original cgroup so teardown can put it
// back, mirroring how route_linux.go remembers what it added. The mutex is for
// the rescan: it runs on the health-check goroutine while teardown may be
// emptying the map from the session's.
var (
	splitMu   sync.Mutex
	splitHome = map[string]string{}
	// splitUnclosed holds the moved PIDs whose connections from before the move
	// killLeaked could not close. The rescan never retries them — by then their
	// new sockets are tunnelled, and a kill would hit those too — so this is what
	// keeps them reported until the process exits.
	splitUnclosed = map[string]bool{}
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

// ownedBy reports whether user uid runs the process, by its real and
// effective ids both: a setuid program the user started is not the user's.
func ownedBy(pid string, uid int) bool {
	data, err := os.ReadFile(filepath.Join(procRoot, pid, "status"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(v)
			return len(f) >= 2 && f[0] == strconv.Itoa(uid) && f[1] == strconv.Itoa(uid)
		}
	}
	return false
}

// commLen is how much of a name /proc/<pid>/comm keeps.
const commLen = 15

// listedName matches a process name against the list, returning the list's
// spelling. A name cut to comm's 15 characters matches the listed name it
// begins: that is all a process of another user shows when the exe link is
// not readable, as for the service, which has no right to read it.
func listedName(name string, want map[string]string) (string, bool) {
	if name == "" {
		return "", false
	}
	l := strings.ToLower(name)
	if n, ok := want[l]; ok {
		return n, true
	}
	if len(l) == commLen {
		for k, n := range want {
			if strings.HasPrefix(k, l) {
				return n, true
			}
		}
	}
	return "", false
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
	if errors.Is(err, fs.ErrPermission) {
		// Somebody else's process, seen without the right to follow its exe
		// link: comm is all there is. A kernel thread has no exe at all, and
		// says ENOENT.
		comm, cerr := os.ReadFile(filepath.Join(procRoot, pid, "comm"))
		if cerr != nil {
			return ""
		}
		return strings.TrimSpace(string(comm))
	}
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
func EnableSplit(names []string, tcpPort, dnsPort int) (SplitScan, error) {
	return EnableSplitFor(names, tcpPort, dnsPort, -1)
}

// EnableSplitFor is EnableSplit that moves only the processes of user uid, or
// anyone's for -1. The service runs the split for the user who asked (H10):
// another user's processes are not theirs to send into the tunnel.
func EnableSplitFor(names []string, tcpPort, dnsPort, uid int) (SplitScan, error) {
	if len(names) == 0 {
		// A run killed before its teardown (SIGKILL, power loss) leaves the table
		// and a populated cgroup behind, and those processes keep redirecting
		// into a port nobody listens on. This is the only sweep that runs on
		// every start, so an emptied apps.txt has to do the cleaning.
		_ = DisableSplit()
		return SplitScan{}, nil
	}
	// Written down before the cgroup and the ruleset exist: processes of a run
	// killed with them in place keep redirecting into a dead port (H06).
	note(func(j *journal) { j.Split = true })
	if err := os.MkdirAll(splitCgroup, 0750); err != nil {
		return SplitScan{}, fmt.Errorf("создать cgroup %s: %w", splitCgroup, err)
	}

	// Rules before processes: a PID that joins the cgroup while the ruleset is
	// still missing sends everything it opens in that window out untunnelled.
	if err := installSplitRules(tcpPort, dnsPort); err != nil {
		// Leave nothing half-built: the cgroup without rules would silently
		// route nothing while the UI claims the processes are tunnelled.
		_ = DisableSplit()
		return SplitScan{}, err
	}

	scan, err := moveIntoSplit(names, uid)
	if err != nil {
		_ = DisableSplit()
		return SplitScan{}, err
	}
	return scan, nil
}

// installSplitRules puts the split's two chains in our table, replacing any a
// crashed run left, in one batch.
func installSplitRules(tcpPort, dnsPort int) error {
	if err := nftReplace(splitChains, splitRuleset(tcpPort, dnsPort)); err != nil {
		return fmt.Errorf("правила раздельной маршрутизации: %w", err)
	}
	return nil
}

// splitRuleset is the split: a nat chain that redirects the cgroup's traffic
// into xray, and a filter chain that stops what the redirect cannot carry.
func splitRuleset(tcpPort, dnsPort int) []nftChain {
	// The socket expression is the cgroup v2 match; meta cgroup is the v1
	// net_cls classid and does not see this hierarchy. The cgroup is named from
	// the hierarchy root, not by its last component: for the delegated one,
	// "xray-split" alone is a cgroup that does not exist (G05).
	cg, level := strings.TrimPrefix(splitRel(), "/"), splitLevel()
	v4 := func(r nftRule) nftRule { r.cgroup, r.level, r.nfproto = cg, level, 4; return r }
	v6 := func(r nftRule) nftRule { r.cgroup, r.level, r.nfproto = cg, level, 6; return r }

	// DNS goes first, before the bypass: the host's resolver usually *is* a
	// bypassed address (127.0.0.53 for systemd-resolved, the router on a LAN
	// address), so a later rule would never see the query. IPv4 only: the
	// dokodemo listeners are, and IPv6 is refused below.
	nat := []nftRule{v4(nftRule{l4proto: protoUDP, dport: 53, verdict: nftRedirect, port: uint16(dnsPort)})}
	for _, p := range splitDirectNets {
		nat = append(nat, v4(nftRule{daddr: p, verdict: nftReturn}))
	}
	nat = append(nat, v4(nftRule{l4proto: protoTCP, verdict: nftRedirect, port: uint16(tcpPort)}))

	// Only TCP is redirected above, so every other UDP flow — QUIC, a voice
	// call, WebRTC — would walk straight past the tunnel and out of the real
	// interface with the real address. That is a leak, not a missing feature,
	// so it is rejected: QUIC falls back to TCP 443, and the rest fails loudly
	// instead of quietly going direct.
	//
	// A filter chain, not the nat one: nat only sees the first packet of a
	// flow, so a drop there is a side effect of conntrack rather than a rule
	// that plainly holds. Reject over drop for the same reason of clarity — the
	// fallback is immediate instead of waiting out a timeout.
	//
	// The bypass comes first here too: by this hook the redirected DNS is
	// already addressed to 127.0.0.1, and LAN/multicast UDP (mDNS, DHCP, a
	// printer) belongs to the host exactly as it does in the nat chain.
	//
	// ponytail: UDP наружу закрыт целиком; звонки в Telegram перестанут
	// работать, пока процесс в списке. Полноценный UDP через туннель — это
	// TPROXY-инбаунд вместо NAT REDIRECT.
	var block []nftRule
	for _, p := range splitDirectNets {
		block = append(block, v4(nftRule{daddr: p, verdict: nftReturn}))
	}
	block = append(block,
		v4(nftRule{l4proto: protoUDP, verdict: nftRejectICMP, code: 3}), // port-unreachable
		// Страховка от утечки: сюда доходит только TCP, которого nat почему-то не
		// развернул (у развёрнутого daddr уже 127.0.0.1, и его забрал bypass
		// выше). Сброс с RST заставляет приложение переподключиться, а не идти
		// наружу с реальным адресом.
		//
		// Сокеты, открытые ДО переезда процесса в cgroup, это правило не ловит:
		// "socket cgroupv2" читает cgroup из sk_cgrp_data, который ядро
		// проставляет при создании сокета и не обновляет при миграции процесса.
		// Их закрывает killLeaked.
		v4(nftRule{l4proto: protoTCP, verdict: nftRejectTCP}),
	)
	// IPv6 is not redirected at all: the dokodemo listener is v4-only. Left
	// alone, an app on a network with IPv6 simply prefers the AAAA record and
	// every byte leaves untunnelled — the worst leak of the mode, because
	// nothing in the UI hints at it. Rejecting it turns Happy Eyeballs around
	// in milliseconds onto the v4 path, which is redirected.
	for _, p := range splitDirectNets6 {
		block = append(block, v6(nftRule{daddr: p, verdict: nftReturn}))
	}
	block = append(block, v6(nftRule{verdict: nftRejectICMP, code: 1})) // admin-prohibited

	return []nftChain{
		{name: splitNatChain, nat: true, priority: -100, rules: nat},
		{name: splitBlockChain, priority: 0, rules: block},
	}
}

// RefreshSplit re-scans for processes from the list that are not in the cgroup
// yet and moves them in, returning everything the mode now covers. The nft rules
// match the cgroup, not a PID, so a late-starting app needs nothing else — this
// is what makes "start the app after connecting" work without a reconnect.
//
// It is a no-op when the split tunnel is not up: no cgroup, nothing to join.
func RefreshSplit(names []string) (SplitScan, error) {
	return RefreshSplitFor(names, -1)
}

// RefreshSplitFor is RefreshSplit for the processes of user uid (-1: anyone).
func RefreshSplitFor(names []string, uid int) (SplitScan, error) {
	if len(names) == 0 || !fileExists(splitCgroup) {
		return SplitScan{}, nil
	}
	return moveIntoSplit(names, uid)
}

// moveIntoSplit puts every PID whose name is in the list into the split cgroup;
// with uid >= 0, only the processes that user runs.
func moveIntoSplit(names []string, uid int) (SplitScan, error) {
	splitMu.Lock()
	defer splitMu.Unlock()

	want := map[string]string{}
	for _, n := range names {
		want[strings.ToLower(n)] = n
	}

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return SplitScan{}, err
	}

	procs := filepath.Join(splitCgroup, "cgroup.procs")
	found := map[string]bool{}
	moved := map[string]bool{}
	seen := map[string]string{} // pid → name, every listed process in the cgroup now
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if uid >= 0 && !ownedBy(e.Name(), uid) {
			continue
		}
		name, ok := listedName(procName(e.Name()), want)
		if !ok {
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
		if home != splitRel() {
			// Процесс только что переехал снаружи: его старые сокеты мимо туннеля.
			moved[e.Name()] = true
			// A PID reused by a new process says nothing of the old one's sockets.
			delete(splitUnclosed, e.Name())
			if home != "" {
				splitHome[e.Name()] = home
			}
		}
		found[name] = true
		seen[e.Name()] = name
	}
	if len(moved) > 0 {
		// Where they came from, so a teardown after a crash can put them back.
		home := maps.Clone(splitHome)
		note(func(j *journal) { j.SplitHome = home })
	}
	maps.Copy(splitUnclosed, killLeaked(moved))
	unclosed := map[string]bool{}
	for pid := range splitUnclosed {
		name, ok := seen[pid]
		if !ok {
			// The process exited, and its sockets with it.
			delete(splitUnclosed, pid)
			continue
		}
		unclosed[name] = true
	}

	out := make([]string, 0, len(found))
	for n := range found {
		out = append(out, n)
	}
	sort.Strings(out)
	return SplitScan{Matched: out, Unclosed: slices.Sorted(maps.Keys(unclosed))}, nil
}

// killLeaked closes the TCP connections the given processes had open before they
// joined the cgroup. Those sockets are invisible to the whole ruleset: nft's
// "socket cgroupv2" reads sk_cgrp_data, which the kernel fills in when the socket
// is created and never updates when the process migrates. So a long-lived
// keep-alive — Claude Code holds HTTP/2 to api.anthropic.com for hours — keeps
// going out with the real address while the log shows new flows through the
// redirect, and the site answers by country: "connected, then asks to log in".
//
// Closing the socket is the only lever left; the app reconnects at once and the
// new socket is matched. Only PIDs that came from outside the cgroup are passed
// in, so the one-second rescan cannot keep killing what it already tunnelled.
//
// It returns the PIDs whose old connections it could not close: those keep going
// past the tunnel until the app reconnects on its own, and saying nothing would
// leave the process row reading as "all of it tunnelled" (14b).
func killLeaked(pids map[string]bool) map[string]bool {
	if len(pids) == 0 {
		return nil
	}
	unclosed := map[string]bool{}
	// ss prints the process next to each socket, which saves mapping inode
	// numbers out of /proc/<pid>/fd ourselves.
	out, err := ssCmd.run("ss", "-tnHp", "state", "established")
	if err != nil {
		// Without the list nothing was closed, and nothing says there was
		// nothing to close.
		slog.Warn("split: old connections not closed, they bypass the tunnel", "pids", slices.Sorted(maps.Keys(pids)), "error", err)
		for pid := range pids {
			unclosed[pid] = true
		}
		return unclosed
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[len(f)-1], "users:") {
			continue
		}
		local, peer, users := f[len(f)-3], f[len(f)-2], f[len(f)-1]
		owners := socketOwners(users, pids)
		if len(owners) == 0 || !routable(peer) {
			continue
		}
		// ss -K is no answer on its own: it exits 0 when the kernel refuses (no
		// CAP_NET_ADMIN, no CONFIG_INET_DIAG_DESTROY) and prints nothing then —
		// nor when the connection ended by itself meanwhile, which is no leak.
		// Whether the connection is still up afterwards is the answer.
		res, err := ssCmd.run("ss", "-K", "state", "established", "src", local, "dst", peer)
		if !stillOpen(local, peer) {
			slog.Debug("split: closed pre-existing connection", "src", local, "dst", peer)
			continue
		}
		slog.Warn("split: old connection not closed, it bypasses the tunnel",
			"pids", owners, "src", local, "dst", peer, "error", err, "ss", strings.TrimSpace(string(res)))
		for _, pid := range owners {
			unclosed[pid] = true
		}
	}
	return unclosed
}

// stillOpen reports whether ss still lists the connection local → peer. A
// listing that fails counts as open: nothing shows the connection is gone.
func stillOpen(local, peer string) bool {
	out, err := ssCmd.run("ss", "-tnH", "state", "established", "src", local, "dst", peer)
	if err != nil {
		return true
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if f := strings.Fields(line); slices.Contains(f, local) && slices.Contains(f, peer) {
			return true
		}
	}
	return false
}

// socketOwners returns the given PIDs that ss's users:(("name",pid=N,fd=M),…)
// column names: a socket inherited over fork has several.
func socketOwners(users string, pids map[string]bool) []string {
	var owners []string
	for _, part := range strings.Split(users, "pid=")[1:] {
		if pid, _, _ := strings.Cut(part, ","); pids[pid] {
			owners = append(owners, pid)
		}
	}
	return owners
}

// routable is the ss-address counterpart of the splitDirectNets bypass: only
// connections that would have been redirected are worth closing. Killing the
// loopback ones would cut the app off from its own editor or IPC socket.
func routable(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// DisableSplit removes the ruleset and empties the cgroup. It is safe to call
// when nothing was ever enabled, which is what every teardown path does.
func DisableSplit() error {
	splitMu.Lock()
	defer splitMu.Unlock()

	// Nothing there is the expected case on a clean run, not an error. What
	// would not come out is logged and the cgroup is emptied regardless: a
	// ruleset matching an empty cgroup redirects nothing.
	if err := nftRemove(splitChains); err != nil {
		slog.Warn("split: nftables chains not removed", "error", err)
	}

	if _, err := os.Stat(splitCgroup); err != nil {
		note(func(j *journal) { j.Split, j.SplitHome = false, nil })
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
	clear(splitUnclosed)
	// RemoveAll, not Remove: on cgroupfs the directory goes in one rmdir once it
	// is empty of processes, and the fallback recursion is what makes the same
	// call work on an ordinary directory under test.
	if err := os.RemoveAll(splitCgroup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("удалить cgroup %s: %w", splitCgroup, err)
	}
	note(func(j *journal) { j.Split, j.SplitHome = false, nil })
	return nil
}
