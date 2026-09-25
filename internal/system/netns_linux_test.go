//go:build linux

package system

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// The live tests change the network of the namespace they run in, so they run
// only when asked to, in a namespace of their own:
//
//	sudo env XRAY_RUNNER_NETNS=1 unshare -n go test -run Live ./internal/system/
//
// The CI does that on every push (ci.yml). What they check is what the fakes
// cannot: that the kernel reads our netlink and nftables messages the way the
// models assume (H09).
func requireNetns(t *testing.T) {
	t.Helper()
	if os.Getenv("XRAY_RUNNER_NETNS") != "1" {
		t.Skip("set XRAY_RUNNER_NETNS=1 and run under `unshare -n` as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("XRAY_RUNNER_NETNS=1 needs root")
	}
	// A namespace of our own has nothing but a down loopback in it; anything
	// else means this is somebody's real network.
	links, err := netlink.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if n := l.Attrs().Name; n != "lo" && !strings.HasPrefix(n, "live-") && n != "xray-tun" {
			t.Fatalf("interface %s: this is not a fresh network namespace", n)
		}
	}
	journalAt = nil
	t.Cleanup(func() { journalAt = &memStore{m: map[int][]byte{}} })
}

// liveHost builds the machine the routing tests model: a physical uplink with
// a default route and a tun with xray's addresses.
func liveHost(t *testing.T) {
	t.Helper()
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	must(t, netlink.LinkSetUp(lo))
	wan := addDummy(t, "live-wan", "192.168.31.94/24")
	must(t, netlink.RouteAdd(&netlink.Route{LinkIndex: wan.Attrs().Index, Gw: net.ParseIP("192.168.31.1")}))
	tun := addDummy(t, "xray-tun", "10.0.0.1/24")
	if HasIPv6Stack() {
		addr, err := netlink.ParseAddr("fdfe:dcba:9876::1/126")
		must(t, err)
		must(t, netlink.AddrAdd(tun, addr))
	}
	t.Cleanup(func() {
		_ = netlink.LinkDel(tun)
		_ = netlink.LinkDel(wan)
	})
}

// addDummy adds an interface that is up with a carrier: one end of a veth pair,
// the other end left up and unaddressed. A veth needs no module the smallest
// kernels leave out, as the dummy one does.
func addDummy(t *testing.T, name, cidr string) netlink.Link {
	t.Helper()
	peer := "live-p" + strings.TrimPrefix(strings.TrimPrefix(name, "live-"), "xray-")
	l := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peer}
	must(t, netlink.LinkAdd(l))
	p, err := netlink.LinkByName(peer)
	must(t, err)
	must(t, netlink.LinkSetUp(p))
	link, err := netlink.LinkByName(name)
	must(t, err)
	addr, err := netlink.ParseAddr(cidr)
	must(t, err)
	must(t, netlink.AddrAdd(link, addr))
	must(t, netlink.LinkSetUp(link))
	return link
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// hostState is every route and rule, for before/after comparisons.
func hostState(t *testing.T) ([]ipRoute, []ipRoute, []ipRule) {
	t.Helper()
	var ops netlinkOps
	r4, err := ops.listRoutes(false)
	must(t, err)
	var r6 []ipRoute
	if HasIPv6Stack() {
		r6, err = ops.listRoutes(true)
		must(t, err)
	}
	rules, err := ops.listRules()
	must(t, err)
	return r4, r6, rules
}

func TestLiveTunRouting(t *testing.T) {
	requireNetns(t)
	liveHost(t)
	installed = nil
	t.Cleanup(func() { _ = DisableTunRouting() })

	before4, before6, beforeRules := hostState(t)
	cfg := TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235", "192.168.31.7"}, Addr6: "fdfe:dcba:9876::1"}
	if err := EnableTunRouting(cfg); err != nil {
		t.Fatalf("EnableTunRouting: %v", err)
	}

	var ops netlinkOps
	r4, err := ops.listRoutes(false)
	must(t, err)
	for _, want := range []ipRoute{
		{Dst: "0.0.0.0/1", Dev: "xray-tun", Metric: tunMetric},
		{Dst: "128.0.0.0/1", Dev: "xray-tun", Metric: tunMetric},
		{Dst: "45.150.32.235", Gateway: "192.168.31.1", Dev: "live-wan", Metric: tunMetric},
		{Dst: "192.168.31.7", Dev: "live-wan", Metric: tunMetric},
		{Dst: "default", Gateway: "192.168.31.1", Dev: "live-wan", Table: directTable, Metric: tunMetric},
	} {
		if !slices.Contains(r4, want) {
			t.Errorf("no route %+v in %+v", want, r4)
		}
	}
	rules, err := ops.listRules()
	must(t, err)
	if !slices.ContainsFunc(rules, func(r ipRule) bool {
		return r.FwMark == "0xff" && r.FwMask == "" && r.Table == directTable
	}) {
		t.Errorf("no mark rule in %+v", rules)
	}
	if HasIPv6Stack() {
		r6, err := ops.listRoutes(true)
		must(t, err)
		for _, half := range []string{"::/1", "8000::/1"} {
			want := ipRoute{Dst: half, Dev: "lo", Metric: 1024, Type: "7"}
			if !slices.Contains(r6, want) {
				t.Errorf("no IPv6 block %+v in %+v", want, r6)
			}
		}
	}
	if _, dev, err := ops.routeGet(netip.MustParseAddr("1.1.1.1")); err != nil || dev != "xray-tun" {
		t.Errorf("1.1.1.1 goes to %q (%v), want xray-tun", dev, err)
	}
	if via, dev, err := ops.routeGet(netip.MustParseAddr("45.150.32.235")); err != nil || dev != "live-wan" || via != "192.168.31.1" {
		t.Errorf("the server goes via %q dev %q (%v), want the physical path", via, dev, err)
	}

	if err := DisableTunRouting(); err != nil {
		t.Fatalf("DisableTunRouting: %v", err)
	}
	after4, after6, afterRules := hostState(t)
	if !slices.Equal(before4, after4) || !slices.Equal(before6, after6) || !slices.Equal(beforeRules, afterRules) {
		t.Errorf("the host did not come back as it was:\nbefore %+v\n       %+v\n       %+v\nafter  %+v\n       %+v\n       %+v",
			before4, before6, beforeRules, after4, after6, afterRules)
	}
}

// A route another client holds on one of our prefixes, at another metric, is
// refused before the first change and survives the teardown (A08, F02).
func TestLiveTunRoutingLeavesForeignRoutes(t *testing.T) {
	requireNetns(t)
	liveHost(t)
	installed = nil
	t.Cleanup(func() { _ = DisableTunRouting() })

	tun, err := netlink.LinkByName("xray-tun")
	must(t, err)
	_, half, _ := net.ParseCIDR("0.0.0.0/1")
	foreign := &netlink.Route{Dst: half, LinkIndex: tun.Attrs().Index, Priority: 50, Scope: unix.RT_SCOPE_LINK}
	must(t, netlink.RouteAdd(foreign))
	before4, _, beforeRules := hostState(t)

	cfg := TunRouteConfig{Iface: "xray-tun", Addr: "10.0.0.1", ServerIPs: []string{"45.150.32.235"}}
	if err := EnableTunRouting(cfg); err == nil {
		t.Fatal("EnableTunRouting went ahead over a foreign route on its prefix")
	}
	after4, _, afterRules := hostState(t)
	if !slices.Equal(before4, after4) || !slices.Equal(beforeRules, afterRules) {
		t.Errorf("a refused setup changed the host:\nbefore %+v\nafter  %+v", before4, after4)
	}
}

// The kernel's own answer to a route that is already there is an error, and
// a delete of one that is gone is one too: the model's add and del hold.
func TestLiveNetlinkAddDel(t *testing.T) {
	requireNetns(t)
	liveHost(t)
	var ops netlinkOps
	e := tunEntry{prefix: netip.MustParsePrefix("0.0.0.0/1"), dev: "xray-tun", metric: tunMetric}
	must(t, ops.add(e))
	if err := ops.add(e); !errors.Is(err, unix.EEXIST) {
		t.Errorf("second add: %v, want EEXIST", err)
	}
	// Another metric is another route: the delete must not reach it.
	other := e
	other.metric = 7
	must(t, ops.add(other))
	must(t, ops.del(e))
	if err := ops.del(e); err == nil {
		t.Error("deleting a route that is gone succeeded")
	}
	r4, err := ops.listRoutes(false)
	must(t, err)
	if !slices.Contains(r4, ipRoute{Dst: "0.0.0.0/1", Dev: "xray-tun", Metric: 7}) {
		t.Errorf("the delete took the route of another metric: %+v", r4)
	}
	must(t, ops.del(other))
}

// sendUDP sends one datagram to addr, with the socket mark when mark is set,
// and returns what sendto said: the output hook's drop comes back as EPERM.
func sendUDP(t *testing.T, addr string, mark int) error {
	t.Helper()
	ap := netip.MustParseAddrPort(addr)
	fam := unix.AF_INET
	if ap.Addr().Is6() {
		fam = unix.AF_INET6
	}
	fd, err := unix.Socket(fam, unix.SOCK_DGRAM, 0)
	must(t, err)
	defer unix.Close(fd)
	if mark != 0 {
		must(t, unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_MARK, mark))
	}
	var sa unix.Sockaddr
	if ap.Addr().Is4() {
		sa = &unix.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}
	} else {
		sa = &unix.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}
	}
	return unix.Sendto(fd, []byte("x"), 0, sa)
}

// The kill switch as the kernel runs it: what it lets through and what it
// stops, and nothing of it left once it is down.
func TestLiveKillSwitch(t *testing.T) {
	requireNetns(t)
	liveHost(t)
	t.Cleanup(func() { _ = nftDropTable() })

	if err := sendUDP(t, "8.8.8.8:9", 0); err != nil {
		t.Fatalf("before the kill switch a datagram out fails: %v", err)
	}
	cfg := KillSwitchConfig{Endpoints: []Endpoint{{IP: "203.0.113.5", Port: 4443, UDP: true}}}
	if err := EnableKillSwitch(cfg); err != nil {
		t.Fatalf("EnableKillSwitch: %v", err)
	}
	for _, c := range []struct {
		name string
		addr string
		mark int
		pass bool
	}{
		{"past the tunnel", "8.8.8.8:9", 0, false},
		{"through the tunnel", "10.0.0.2:9", 0, true},
		{"marked direct", "8.8.8.8:9", 255, true},
		{"the server", "203.0.113.5:4443", 0, true},
		{"the server, other port", "203.0.113.5:4444", 0, false},
		{"DNS", "8.8.8.8:53", 0, true},
		{"loopback", "127.0.0.1:9", 0, true},
	} {
		err := sendUDP(t, c.addr, c.mark)
		if c.pass && err != nil {
			t.Errorf("%s: %v, want it through", c.name, err)
		}
		if !c.pass && !errors.Is(err, unix.EPERM) {
			t.Errorf("%s: %v, want EPERM", c.name, err)
		}
	}
	// A second enable replaces the chain rather than stacking another.
	if err := EnableKillSwitch(cfg); err != nil {
		t.Fatalf("second EnableKillSwitch: %v", err)
	}
	chains, err := nfnetlink{}.chains()
	must(t, err)
	if !slices.Equal(chains, []string{killSwitchChain}) {
		t.Errorf("chains after two enables: %v", chains)
	}

	if err := DisableKillSwitch(); err != nil {
		t.Fatalf("DisableKillSwitch: %v", err)
	}
	if chains, err := (nfnetlink{}).chains(); err != nil || chains != nil {
		t.Errorf("after the kill switch went: chains %v, %v; want no table", chains, err)
	}
	if err := sendUDP(t, "8.8.8.8:9", 0); err != nil {
		t.Errorf("after the kill switch a datagram out still fails: %v", err)
	}
}

// The kernel takes the split's ruleset — inet nat, the cgroup v2 match, the
// redirect and both rejects — and it comes out whole.
func TestLiveSplitRuleset(t *testing.T) {
	requireNetns(t)
	root := "/sys/fs/cgroup"
	if r := os.Getenv("XRAY_RUNNER_CGROUP2"); r != "" {
		root = r // a cgroup2 mounted elsewhere, for a container without one
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil || st.Type != unix.CGROUP2_SUPER_MAGIC {
		t.Skip("no cgroup v2 hierarchy at " + root)
	}
	dir := root + "/xray-live-test"
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		t.Skipf("cannot make a cgroup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	origRoot, origSplit := cgroupRoot, splitCgroup
	cgroupRoot, splitCgroup = root, dir
	t.Cleanup(func() { cgroupRoot, splitCgroup = origRoot, origSplit; _ = nftDropTable() })

	if err := installSplitRules(10810, 10853); err != nil {
		t.Fatalf("installSplitRules: %v", err)
	}
	if err := EnableKillSwitch(KillSwitchConfig{}); err != nil {
		t.Fatalf("EnableKillSwitch beside the split: %v", err)
	}
	chains, err := nfnetlink{}.chains()
	must(t, err)
	slices.Sort(chains)
	if want := []string{killSwitchChain, splitBlockChain, splitNatChain}; !slices.Equal(chains, want) {
		t.Errorf("chains %v, want %v", chains, want)
	}
	must(t, nftRemove(splitChains))
	chains, err = nfnetlink{}.chains()
	must(t, err)
	if !slices.Equal(chains, []string{killSwitchChain}) {
		t.Errorf("after the split went: %v, want the kill switch alone", chains)
	}
	must(t, DisableKillSwitch())
}

// Tables a version before H09 made go in the recovery.
func TestLiveDropLegacySplit(t *testing.T) {
	requireNetns(t)
	c, err := nftables.New()
	must(t, err)
	for _, fam := range []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6} {
		c.AddTable(&nftables.Table{Name: legacySplitTable, Family: fam})
	}
	must(t, c.Flush())
	must(t, dropLegacySplit())
	for _, fam := range []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6} {
		tables, err := c.ListTablesOfFamily(fam)
		must(t, err)
		if len(tables) != 0 {
			t.Errorf("family %v: tables left %v", fam, tables)
		}
	}
	must(t, dropLegacySplit()) // nothing there is fine
}
