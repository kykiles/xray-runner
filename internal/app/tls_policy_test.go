package app

// A01 from the app's side: whichever builder a config comes from — a link, a
// preserved outbound, a whole profile, one server of it, the template fallback,
// the preview or a ping — what the core receives obeys ALLOW_INSECURE.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/xraycfg"
)

// insecureRaw is a preserved outbound asking to skip certificate checks.
const insecureRaw = `{"tag": "proxy", "protocol": "trojan",
  "settings": {"servers": [{"address": "a.invalid", "port": 443, "password": "dummy"}]},
  "streamSettings": {"network": "tcp", "security": "tls",
    "tlsSettings": {"serverName": "a.invalid", "allowInsecure": true, "futureKnob": "keep"}}}`

// chainProxy2 leaves through chainHop, and both ask to skip checks; the
// profile's first outbound does not.
const chainProxy2 = `{"tag": "proxy-2", "protocol": "trojan",
  "settings": {"servers": [{"address": "b.invalid", "port": 443, "password": "dummy"}]},
  "streamSettings": {"network": "tcp", "security": "tls",
    "tlsSettings": {"serverName": "b.invalid", "allowInsecure": true, "futureKnob": "keep"},
    "sockopt": {"dialerProxy": "hop"}}}`

const chainHop = `{"tag": "hop", "protocol": "trojan",
  "settings": {"servers": [{"address": "c.invalid", "port": 443, "password": "dummy"}]},
  "streamSettings": {"network": "tcp", "security": "tls",
    "tlsSettings": {"serverName": "c.invalid", "allowInsecure": true}}}`

const insecureChain = `{
  "remarks": "Chain",
  "outbounds": [
    {"tag": "proxy", "protocol": "trojan",
     "settings": {"servers": [{"address": "a.invalid", "port": 443, "password": "dummy"}]},
     "streamSettings": {"network": "tcp", "security": "tls", "tlsSettings": {"serverName": "a.invalid"}}},
    ` + chainProxy2 + `,
    ` + chainHop + `,
    {"tag": "direct", "protocol": "freedom"}
  ],
  "routing": {
    "balancers": [{"tag": "Balancer", "selector": ["proxy"]}],
    "rules": [{"type": "field", "network": "tcp,udp", "balancerTag": "Balancer"}]
  }
}`

// noRoutingProfile carries no routing of its own, so its server runs on the
// template.
const noRoutingProfile = `{"outbounds": [` + insecureRaw + `]}`

func linkEntry() *subscription.SubEntry {
	return &subscription.SubEntry{
		Protocol: "trojan", Address: "a.invalid", Port: 443, Password: "dummy",
		Security: "tls", SNI: "a.invalid", Insecure: true,
	}
}

func rawEntry(outbound string) *subscription.SubEntry {
	return &subscription.SubEntry{
		Protocol: "trojan", Address: "a.invalid", Port: 443, Password: "dummy",
		RawOutbound: json.RawMessage(outbound),
	}
}

// countInsecure counts every "allowInsecure": true anywhere in the config.
func countInsecure(t *testing.T, raw []byte) int {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("config is not JSON: %v", err)
	}
	var walk func(any) int
	walk = func(v any) int {
		n := 0
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "allowInsecure" && x == true {
					n++
				}
				n += walk(x)
			}
		case []any:
			for _, x := range v {
				n += walk(x)
			}
		}
		return n
	}
	return walk(v)
}

type policyCase struct {
	name   string
	target func(allow bool) *target
	// insecure is how many flags the source carries into the config; only the
	// local opt-in lets them through.
	insecure int
}

var policyCases = []policyCase{
	{"ссылка", func(allow bool) *target {
		e := linkEntry()
		e.AllowInsecure = allow // what navigate/select do with the local setting
		return &target{entry: e}
	}, 1},
	{"outbound без профиля", func(bool) *target {
		return &target{entry: rawEntry(insecureRaw)}
	}, 1},
	{"профиль целиком", func(bool) *target {
		return &target{profileName: "Chain", profileRaw: json.RawMessage(insecureChain)}
	}, 2},
	{"сервер из профиля", func(bool) *target {
		return &target{profileName: "Chain", entry: rawEntry(chainProxy2), profileRaw: json.RawMessage(insecureChain)}
	}, 2},
	{"шаблон вместо профиля без routing", func(bool) *target {
		return &target{entry: rawEntry(insecureRaw), profileRaw: json.RawMessage(noRoutingProfile)}
	}, 1},
}

func TestSecurityPolicy_EverySessionSource(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, c := range policyCases {
			t.Run(fmt.Sprintf("%s/allow=%v", c.name, allow), func(t *testing.T) {
				a := newTemplateApp(t)
				a.cfg.AllowInsecure = allow
				tgt := c.target(allow)
				var srcProfile, srcOutbound []byte
				srcProfile = bytes.Clone(tgt.profileRaw)
				if tgt.entry != nil {
					srcOutbound = bytes.Clone(tgt.entry.RawOutbound)
				}
				want := 0
				if allow {
					want = c.insecure
				}

				raw, _, err := a.buildSessionConfig(tgt)
				if err != nil {
					t.Fatalf("buildSessionConfig: %v", err)
				}
				if got := countInsecure(t, raw); got != want {
					t.Errorf("session config: %d allowInsecure=true, want %d", got, want)
				}
				if c.name != "ссылка" && !strings.Contains(string(raw), "futureKnob") {
					t.Error("an unknown tlsSettings field was lost")
				}

				preview, err := a.previewConfig(tgt)
				if err != nil {
					t.Fatalf("previewConfig: %v", err)
				}
				if got := countInsecure(t, []byte(preview)); got != want {
					t.Errorf("preview: %d allowInsecure=true, want %d", got, want)
				}

				if !bytes.Equal(tgt.profileRaw, srcProfile) {
					t.Error("the profile's bytes were modified")
				}
				if tgt.entry != nil && !bytes.Equal(tgt.entry.RawOutbound, srcOutbound) {
					t.Error("the entry's preserved outbound was modified")
				}
				if !allow {
					// The core itself has to take what is left.
					validateWithXray(t, raw)
				}
			})
		}
	}
}

// A value the core may read either way stops the connection. For a server of a
// profile this also means no quiet fallback to the template, where the same
// outbound would carry it along.
func TestSecurityPolicy_AmbiguousValueStopsTheSession(t *testing.T) {
	bad := strings.Replace(chainProxy2, `"allowInsecure": true`, `"allowInsecure": "true"`, 1)
	badChain := strings.Replace(insecureChain, chainProxy2, bad, 1)
	targets := map[string]*target{
		"outbound без профиля": {entry: rawEntry(bad)},
		"профиль целиком":      {profileName: "Chain", profileRaw: json.RawMessage(badChain)},
		"сервер из профиля":    {profileName: "Chain", entry: rawEntry(bad), profileRaw: json.RawMessage(badChain)},
	}
	for name, tgt := range targets {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allow=%v", name, allow), func(t *testing.T) {
				a := newTemplateApp(t)
				a.cfg.AllowInsecure = allow
				raw, _, err := a.buildSessionConfig(tgt)
				if err == nil || !strings.Contains(err.Error(), "allowInsecure") {
					t.Errorf("err = %v (config %d bytes), want a refusal naming allowInsecure", err, len(raw))
				}
			})
		}
	}
}

type benchCase struct {
	name     string
	measure  func(pb *ProxyBenchmarker, ports portPair, dir string) subscription.BenchmarkResult
	insecure int
}

func measureServer(e *subscription.SubEntry) func(*ProxyBenchmarker, portPair, string) subscription.BenchmarkResult {
	return func(pb *ProxyBenchmarker, ports portPair, dir string) subscription.BenchmarkResult {
		return pb.measureOne(context.Background(), *e, ports, dir)
	}
}

func profileServer(outbound, profile string) *subscription.SubEntry {
	e := rawEntry(outbound)
	e.ProfileRaw = json.RawMessage(profile)
	return e
}

var benchCases = []benchCase{
	{"ссылка", measureServer(linkEntry()), 1},
	{"outbound без профиля", measureServer(rawEntry(insecureRaw)), 1},
	{"сервер из профиля", measureServer(profileServer(chainProxy2, insecureChain)), 2},
	{"шаблон вместо профиля без routing", measureServer(profileServer(insecureRaw, noRoutingProfile)), 1},
	{"профиль целиком", func(pb *ProxyBenchmarker, ports portPair, dir string) subscription.BenchmarkResult {
		return pb.measureProfile(context.Background(), subscription.Profile{Name: "Chain", Raw: json.RawMessage(insecureChain)}, ports, dir)
	}, 2},
}

// The ping runs its own configs; what is checked is the file the core was
// started with, not what a builder returned.
func TestSecurityPolicy_BenchmarkConfigReachesCore(t *testing.T) {
	bin := buildSessionXray(t)
	tc, err := xraycfg.LoadTemplate(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	for _, allow := range []bool{false, true} {
		for _, c := range benchCases {
			t.Run(fmt.Sprintf("%s/allow=%v", c.name, allow), func(t *testing.T) {
				record := filepath.Join(t.TempDir(), "core.json")
				t.Setenv("MOCK_XRAY_RECORD", record)
				cfg := benchTestConfig(300 * time.Millisecond)
				cfg.AllowInsecure = allow
				pb := NewProxyBenchmarker(tc, bin, cfg)
				ports, err := freePortPair()
				if err != nil {
					t.Fatalf("freePortPair: %v", err)
				}

				// The mock core never listens, so every measurement ends here.
				res := c.measure(pb, ports, t.TempDir())
				if !errors.Is(res.Error, subscription.ErrCoreNotReady) {
					t.Fatalf("result = %v, want the core started and never ready", res.Error)
				}
				ran, err := os.ReadFile(record)
				if err != nil {
					t.Fatalf("the core recorded no config: %v", err)
				}
				want := 0
				if allow {
					want = c.insecure
				}
				if got := countInsecure(t, ran); got != want {
					t.Errorf("config the core ran: %d allowInsecure=true, want %d", got, want)
				}
			})
		}
	}
}

// A ping of a config the policy refuses fails before anything is started.
func TestSecurityPolicy_BenchmarkRefusesBeforeStart(t *testing.T) {
	record := filepath.Join(t.TempDir(), "core.json")
	t.Setenv("MOCK_XRAY_RECORD", record)
	pb := &ProxyBenchmarker{xrayBinary: buildSessionXray(t), timeout: 300 * time.Millisecond}
	ports, err := freePortPair()
	if err != nil {
		t.Fatalf("freePortPair: %v", err)
	}
	bad := `{"outbounds": [{"tag": "proxy", "streamSettings": {"security": "tls", "tlsSettings": {"allowInsecure": "true"}}}]}`
	res := pb.runAndMeasure(context.Background(), []byte(bad), ports, t.TempDir())
	if res.Error == nil || !strings.Contains(res.Error.Error(), "allowInsecure") {
		t.Errorf("result = %v, want a refusal naming allowInsecure", res.Error)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the core was started on a refused config (stat err = %v)", err)
	}
}

// coreRemovedInsecure is what xray 26.7.28 says about allowInsecure=true.
const coreRemovedInsecure = `Failed to start: main: failed to load config files: [c.json] > infra/conf: failed to build outbound config with tag proxy > infra/conf: Failed to build TLS config. > common/errors: The feature "allowInsecure" has been removed and migrated to "pinnedPeerCertSha256"(pcs) and "verifyPeerCertByName"(vcn). Please update your config(s) according to release note and documentation.`

const insecureHint = "ядро не поддерживает allowInsecure"

// ALLOW_INSECURE=true on a core that dropped the setting is a refusal the user
// has to act on, and the core's own words alone do not say what to do.
func TestSecurityPolicy_CoreWithoutInsecureIsExplained(t *testing.T) {
	t.Setenv("MOCK_XRAY_REFUSE", coreRemovedInsecure)

	t.Run("сессия", func(t *testing.T) {
		var unrouted int
		a := newCoreSessionApp(t, &unrouted, make(chan struct{}))
		_, err := a.runSession(context.Background(), coreSessionTarget())
		if err == nil || !strings.Contains(err.Error(), insecureHint) || !strings.Contains(err.Error(), "has been removed") {
			t.Errorf("runSession err = %v, want the hint and the core's words", err)
		}
	})

	t.Run("замер", func(t *testing.T) {
		pb := &ProxyBenchmarker{xrayBinary: buildSessionXray(t), timeout: 300 * time.Millisecond}
		ports, err := freePortPair()
		if err != nil {
			t.Fatalf("freePortPair: %v", err)
		}
		res := pb.runAndMeasure(context.Background(), []byte(`{}`), ports, t.TempDir())
		if !errors.Is(res.Error, subscription.ErrConfigRejected) || res.String() != "config" {
			t.Fatalf("result = %v (%q), want a rejected config", res.Error, res.String())
		}
		if !strings.Contains(res.Error.Error(), insecureHint) || !strings.Contains(res.Error.Error(), "has been removed") {
			t.Errorf("error = %v, want the hint and the core's words", res.Error)
		}
	})

	t.Run("другой отказ без подсказки", func(t *testing.T) {
		t.Setenv("MOCK_XRAY_REFUSE", `infra/conf: unknown outbound protocol "vless-next"`)
		pb := &ProxyBenchmarker{xrayBinary: buildSessionXray(t), timeout: 300 * time.Millisecond}
		ports, err := freePortPair()
		if err != nil {
			t.Fatalf("freePortPair: %v", err)
		}
		res := pb.runAndMeasure(context.Background(), []byte(`{}`), ports, t.TempDir())
		if res.Error == nil || strings.Contains(res.Error.Error(), insecureHint) {
			t.Errorf("error = %v, want the refusal without the allowInsecure hint", res.Error)
		}
	})
}
