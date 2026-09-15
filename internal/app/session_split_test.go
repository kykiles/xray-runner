package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
	"xray-runner/internal/tui"
	"xray-runner/internal/xraycfg"
)

// splitTarget is a plain server on the template.json path — the split tunnel is
// about inbounds, so the outbound source does not matter here.
func splitTarget() *target {
	return &target{entry: &subscription.SubEntry{
		Protocol: "vless", Address: "b.invalid", Port: 443,
		UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}}
}

func inboundTags(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var got struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	tags := make([]string, 0, len(got.Inbounds))
	for _, in := range got.Inbounds {
		tags = append(tags, in.Tag)
	}
	return tags
}

// With no apps listed, the config must be exactly what it always was: an empty
// apps.txt is the default state for every existing user.
func TestBuildSessionConfig_NoSplitAppsKeepsInbounds(t *testing.T) {
	a := newTemplateApp(t)

	raw, ports, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	tags := inboundTags(t, raw)
	if slices.Contains(tags, "redirect") || slices.Contains(tags, "redirect-dns") {
		t.Errorf("inbounds = %v, want no redirect listeners without apps.txt", tags)
	}
	if ports.socks == 0 || ports.http == 0 {
		t.Errorf("ports = %+v, want the socks/http pair intact", ports)
	}
}

// The redirect listeners are added alongside SOCKS/HTTP, and must not be
// mistaken for them: portsFromInbounds matches by protocol and tag, and a
// dokodemo-door picked up as the HTTP port would break the system proxy.
func TestBuildSessionConfig_SplitAppsAddRedirectInbounds(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("split rides on the tunnel here; redirect listeners are the firewall-built split")
	}
	a := newTemplateApp(t)
	a.splitApps = []string{"code"}
	a.resolveSplit()

	raw, ports, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	tags := inboundTags(t, raw)
	if !slices.Contains(tags, "redirect") || !slices.Contains(tags, "redirect-dns") {
		t.Fatalf("inbounds = %v, want both redirect listeners", tags)
	}
	if ports.socks == 0 || ports.http == 0 {
		t.Errorf("ports = %+v, want the socks/http pair intact", ports)
	}
	if ports.http == xraycfg.RedirectPort || ports.socks == xraycfg.RedirectPort {
		t.Errorf("ports = %+v, a redirect listener was taken for the proxy pair", ports)
	}
	validateWithXray(t, raw)
}

// TUN already carries the whole system; adding a redirect there would be a
// second path to the same place. The probe inbound is the one listener tun has.
func TestBuildSessionConfig_TunIgnoresSplitApps(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	a.splitApps = []string{"code"}

	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	tags := inboundTags(t, raw)
	if !slices.Equal(tags, []string{"tun", "probe"}) {
		t.Errorf("inbounds = %v, want the tun and probe inbounds only", tags)
	}
}

// Teardown must undo the split whenever bring-up enabled it, and leave it alone
// otherwise — the same rule the kill switch and the proxy follow.
func TestReleaseSession_DisablesSplitOnlyWhenEnabled(t *testing.T) {
	a := newTemplateApp(t)
	calls := 0
	a.disableSplit = func() error { calls++; return nil }

	a.releaseSession()
	if calls != 0 {
		t.Errorf("disableSplit called %d times without a split tunnel", calls)
	}

	a.splitOn = true
	a.releaseSession()
	if calls != 1 {
		t.Errorf("disableSplit called %d times, want 1", calls)
	}
	if a.splitOn {
		t.Error("splitOn still set after teardown; a second teardown would undo it twice")
	}
}

// A split that could not start for lack of privileges gets the same advice TUN
// gives; every other failure keeps its own message.
func TestSplitNeedsRoot(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("nft add table ip xray_split: exit status 1: Error: Could not process rule: Operation not permitted"), true},
		{fmt.Errorf("создать cgroup /sys/fs/cgroup/xray-split: %w", os.ErrPermission), true},
		{errors.New("nftables не найден: exec: \"nft\": executable file not found in $PATH"), false},
	}
	for _, c := range cases {
		if got := splitNeedsRoot(c.err); got != c.want {
			t.Errorf("splitNeedsRoot(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// Without a working split the process row is not drawn at all: an empty row
// would read as "nothing is running" when nothing was ever routed.
func TestStatusInfo_SplitRowOnlyWhenEnabled(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "proxy"
	a.splitApps = []string{"claude"}
	ports := sessionPorts{socks: 10808, http: 10809}

	if info := a.statusInfo(splitTarget(), ports); info.Split {
		t.Error("split never started — no process row expected")
	}

	a.setSplitMatched(true, []string{"claude"})
	info := a.statusInfo(splitTarget(), ports)
	if !info.Split || !strings.HasPrefix(info.SplitMode, "SPLIT") {
		t.Errorf("enabled split → %+v, want the row and a SPLIT label", info)
	}
}

// The m key walks proxy ⇄ tun. Split is off the cycle: it is what proxy mode
// does when there are processes to route, not a mode to switch into.
func TestNextModeCycle(t *testing.T) {
	if got := nextMode("proxy"); got != "tun" {
		t.Errorf("nextMode(proxy) = %q, want tun", got)
	}
	if got := nextMode("tun"); got != "proxy" {
		t.Errorf("nextMode(tun) = %q, want proxy", got)
	}
}

// Split turns itself on: proxy mode plus a non-empty list, and nothing else.
// TUN already carries everything, so a list there changes nothing.
func TestResolveSplit(t *testing.T) {
	cases := []struct {
		mode string
		apps []string
		want bool
	}{
		{"proxy", []string{"code"}, true},
		{"proxy", nil, false},
		{"tun", []string{"code"}, false},
	}
	for _, c := range cases {
		a := newTemplateApp(t)
		// resolveSplit asks for privileges, and on Windows that means elevation:
		// without the seam the result depended on which console go test ran from.
		a.checkPrivileges = func() error { return nil }
		a.mode, a.splitApps = c.mode, c.apps
		a.resolveSplit()
		if a.split != c.want {
			t.Errorf("mode=%s apps=%v: split = %v, want %v", c.mode, c.apps, a.split, c.want)
		}
	}
}

// A connection an app opened before it joined the split is outside the rules;
// when it could not be closed, the screen says so — once per app, since the
// rescan keeps reporting it every second until the app exits (14b).
func TestRefreshSplit_NotesUnclosedOnce(t *testing.T) {
	a := newTemplateApp(t)
	a.splitApps = []string{"code", "claude"}
	a.setSplitMatched(true, []string{"code"})
	a.statusCh = make(chan tui.StatusUpdate, 8)

	steps := []struct {
		scan  system.SplitScan
		notes int
	}{
		{system.SplitScan{Matched: []string{"code"}}, 0},
		{system.SplitScan{Matched: []string{"claude", "code"}, Unclosed: []string{"claude"}}, 1},
		// The same claude on the next tick: still open, already said.
		{system.SplitScan{Matched: []string{"claude", "code"}, Unclosed: []string{"claude"}}, 0},
		// claude exited, and its connections with it.
		{system.SplitScan{Matched: []string{"code"}}, 0},
		// A new claude whose old connections survived again is news.
		{system.SplitScan{Matched: []string{"claude", "code"}, Unclosed: []string{"claude"}}, 1},
	}
	for i, s := range steps {
		a.rescanSplit = func([]string) (system.SplitScan, error) { return s.scan, nil }
		a.refreshSplit()

		var notes []string
		for len(a.statusCh) > 0 {
			if u := <-a.statusCh; u.Note != "" {
				notes = append(notes, u.Note)
			}
		}
		if len(notes) != s.notes {
			t.Errorf("step %d: notes = %q, want %d", i, notes, s.notes)
			continue
		}
		for _, n := range notes {
			if !strings.Contains(n, "claude") {
				t.Errorf("step %d: note %q does not name the app", i, n)
			}
		}
	}
}

// Bring-up has no screen yet: an app whose old connections survived the move is
// named in the note the screen opens with, and the first rescan does not say it
// again.
func TestBringUpSplit_NotesUnclosed(t *testing.T) {
	if system.SplitOverTUN {
		t.Skip("split rides on the tunnel here; nothing is moved, nothing to close")
	}
	for _, unclosed := range [][]string{nil, {"code"}} {
		a := newTemplateApp(t)
		a.splitApps = []string{"code"}
		scan := system.SplitScan{Matched: []string{"code"}, Unclosed: unclosed}
		a.enableSplit = func([]string, int, int) (system.SplitScan, error) { return scan, nil }
		a.bringUpSplit()
		if got := strings.Contains(a.pendingNote, "code"); got != (unclosed != nil) {
			t.Errorf("unclosed %v: pendingNote = %q", unclosed, a.pendingNote)
		}

		a.statusCh = make(chan tui.StatusUpdate, 8)
		a.rescanSplit = func([]string) (system.SplitScan, error) { return scan, nil }
		a.refreshSplit()
		if len(a.statusCh) != 0 {
			t.Errorf("unclosed %v: the first rescan sent %+v", unclosed, <-a.statusCh)
		}
	}
}
