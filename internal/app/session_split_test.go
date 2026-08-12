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
	a := newTemplateApp(t)
	a.splitApps = []string{"code"}

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
// second path to the same place.
func TestBuildSessionConfig_TunIgnoresSplitApps(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	a.splitApps = []string{"code"}

	raw, _, err := a.buildSessionConfig(splitTarget())
	if err != nil {
		t.Fatalf("buildSessionConfig: %v", err)
	}
	tags := inboundTags(t, raw)
	if len(tags) != 1 || tags[0] != "tun" {
		t.Errorf("inbounds = %v, want just the tun inbound", tags)
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

// The m key walks proxy → tun → split → proxy, so every mode is reachable
// without editing .env.
func TestNextModeCycle(t *testing.T) {
	want := []string{"tun", "split", "proxy"}
	mode := "proxy"
	for i, w := range want {
		if mode = nextMode(mode); mode != w {
			t.Fatalf("step %d: got %q, want %q", i, mode, w)
		}
	}
}
