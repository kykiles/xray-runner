package xraycfg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// hijackCfg is what the tests read back out of a hijacked config.
type hijackCfg struct {
	DNS struct {
		Hosts map[string][]string `json:"hosts"`
	} `json:"dns"`
	Outbounds []struct {
		Tag      string          `json:"tag"`
		Protocol string          `json:"protocol"`
		Settings json.RawMessage `json:"settings"`
		Stream   *struct {
			Sockopt map[string]any `json:"sockopt"`
		} `json:"streamSettings"`
	} `json:"outbounds"`
	Routing struct {
		Rules []map[string]any `json:"rules"`
	} `json:"routing"`
}

func parseHijacked(t *testing.T, out json.RawMessage) hijackCfg {
	t.Helper()
	var c hijackCfg
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	return c
}

// A panel config with DoH servers: the tunnel's port 53 goes to a dns outbound
// placed after the panel's, by a rule ahead of the panel's; the queries the
// core cannot answer go back through routing by the loopback.
func TestHijackTunDNS_AddsOutboundsAndFirstRule(t *testing.T) {
	raw := json.RawMessage(`{
		"dns":{"hosts":{"dns.google":["8.8.8.8"]},
		       "servers":[{"address":"https://dns.google/dns-query","domains":["geosite:category-ru"]},
		                  "https://1.1.1.1/dns-query","https://dns.adguard-dns.com/dns-query"],
		       "queryStrategy":"UseIPv4"},
		"outbounds":[
			{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443}]}},
			{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"mark":255}}},
			{"tag":"block","protocol":"blackhole"}],
		"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","ip":["geoip:ru"],"outboundTag":"direct"}]}
	}`)
	lookup, asked := fakeLookup(map[string][]string{"dns.adguard-dns.com": {"94.140.14.14", "94.140.15.15"}})

	out, skipped, err := HijackTunDNS(raw, lookup)
	if err != nil || skipped != "" {
		t.Fatalf("HijackTunDNS: skipped=%q err=%v", skipped, err)
	}
	if want := []string{"dns.adguard-dns.com"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("looked up %v, want %v (IPs and the panel's hosts need no lookup)", *asked, want)
	}
	c := parseHijacked(t, out)

	if got := c.DNS.Hosts["full:dns.adguard-dns.com"]; !reflect.DeepEqual(got, []string{"94.140.14.14", "94.140.15.15"}) {
		t.Errorf("adguard pinned as %v", got)
	}
	if got := c.DNS.Hosts["dns.google"]; !reflect.DeepEqual(got, []string{"8.8.8.8"}) || c.DNS.Hosts["full:dns.google"] != nil {
		t.Errorf("panel's dns.google entry changed: %v", c.DNS.Hosts)
	}

	var tags []string
	for _, ob := range c.Outbounds {
		tags = append(tags, ob.Tag)
	}
	if want := []string{"proxy", "direct", "block", TunDNSOutboundTag, tunDNSLoopTag}; !reflect.DeepEqual(tags, want) {
		t.Fatalf("outbounds %v, want %v (the panel's first stays first)", tags, want)
	}
	dnsOut, loop := c.Outbounds[3], c.Outbounds[4]
	if dnsOut.Protocol != "dns" || dnsOut.Stream == nil || dnsOut.Stream.Sockopt["dialerProxy"] != tunDNSLoopTag {
		t.Errorf("dns outbound %+v: want protocol dns dialled through %s", dnsOut, tunDNSLoopTag)
	}
	var dnsSettings struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(dnsOut.Settings, &dnsSettings); err != nil {
		t.Fatal(err)
	}
	if want := []map[string]any{{"action": "hijack", "qType": "1,28"}, {"action": "direct"}}; !reflect.DeepEqual(dnsSettings.Rules, want) {
		t.Errorf("dns outbound rules %v, want %v", dnsSettings.Rules, want)
	}
	var loopSettings map[string]string
	_ = json.Unmarshal(loop.Settings, &loopSettings)
	if loop.Protocol != "loopback" || loopSettings["inboundTag"] != tunDNSForwardTag {
		t.Errorf("loop outbound %+v %v: want loopback into %s", loop, loopSettings, tunDNSForwardTag)
	}

	if len(c.Routing.Rules) != 2 {
		t.Fatalf("rules %v: want the hijack rule plus the panel's one", c.Routing.Rules)
	}
	first := c.Routing.Rules[0]
	if first["port"] != "53" || first["outboundTag"] != TunDNSOutboundTag || !reflect.DeepEqual(first["inboundTag"], []any{TunInboundTag}) {
		t.Errorf("first rule %v: want tun:53 → %s", first, TunDNSOutboundTag)
	}
	if c.Routing.Rules[1]["outboundTag"] != "direct" {
		t.Errorf("panel's rule moved or lost: %v", c.Routing.Rules)
	}

	direct := c.Outbounds[1].Stream.Sockopt
	if direct["domainStrategy"] != pinStrategy || direct["mark"] != float64(255) {
		t.Errorf("direct sockopt %v: want the mark kept and domainStrategy %s", direct, pinStrategy)
	}
	if c.Outbounds[0].Stream != nil {
		t.Errorf("proxy outbound touched: %+v", c.Outbounds[0].Stream)
	}
}

// A freedom outbound the panel already told how to resolve keeps its say.
func TestHijackTunDNS_KeepsPanelFreedomStrategy(t *testing.T) {
	raw := json.RawMessage(`{
		"dns":{"servers":["https://8.8.8.8/dns-query"]},
		"outbounds":[
			{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443}]}},
			{"tag":"d1","protocol":"freedom","settings":{"domainStrategy":"UseIP"}},
			{"tag":"d2","protocol":"freedom","streamSettings":{"sockopt":{"domainStrategy":"ForceIPv6v4"}}},
			{"tag":"d3","protocol":"freedom","settings":{"domainStrategy":"AsIs"}}]
	}`)
	out, skipped, err := HijackTunDNS(raw, func(string) []string { return nil })
	if err != nil || skipped != "" {
		t.Fatalf("HijackTunDNS: skipped=%q err=%v", skipped, err)
	}
	c := parseHijacked(t, out)
	if c.Outbounds[1].Stream != nil {
		t.Errorf("d1 (settings.domainStrategy UseIP) got sockopt %v", c.Outbounds[1].Stream.Sockopt)
	}
	if got := c.Outbounds[2].Stream.Sockopt["domainStrategy"]; got != "ForceIPv6v4" {
		t.Errorf("d2 domainStrategy = %v, want the panel's ForceIPv6v4", got)
	}
	// AsIs is the system resolver, which is what has to go.
	if c.Outbounds[3].Stream == nil || c.Outbounds[3].Stream.Sockopt["domainStrategy"] != pinStrategy {
		t.Errorf("d3 (AsIs) not switched to the core's DNS: %+v", c.Outbounds[3].Stream)
	}
}

// Wherever the core's DNS would ask the system, hijacking the system's DNS
// into it is a loop: the config stays as it was, with the reason.
func TestHijackTunDNS_SkipsWhatAsksTheSystem(t *testing.T) {
	const outbounds = `"outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443}]}},{"tag":"direct","protocol":"freedom"}]`
	cases := map[string]struct {
		cfg    string
		lookup map[string][]string
		reason string
	}{
		"no dns":            {`{` + outbounds + `}`, nil, "нет своих DNS-серверов"},
		"empty servers":     {`{"dns":{"servers":[]},` + outbounds + `}`, nil, "нет своих DNS-серверов"},
		"hosts only":        {`{"dns":{"hosts":{"a.example":["192.0.2.1"]}},` + outbounds + `}`, nil, "нет своих DNS-серверов"},
		"localhost":         {`{"dns":{"servers":["https://8.8.8.8/dns-query","localhost"]},` + outbounds + `}`, nil, `"localhost"`},
		"localhost object":  {`{"dns":{"servers":[{"address":"LocalHost","domains":["geosite:private"]}]},` + outbounds + `}`, nil, `"LocalHost"`},
		"fakedns":           {`{"dns":{"servers":["fakedns","https://8.8.8.8/dns-query"]},` + outbounds + `}`, nil, `"fakedns"`},
		"+local":            {`{"dns":{"servers":["https+local://dns.google/dns-query"]},` + outbounds + `}`, nil, "+local"},
		"no address":        {`{"dns":{"servers":[{"domains":["geosite:google"]}]},` + outbounds + `}`, nil, "спрашивает систему"},
		"unresolved server": {`{"dns":{"servers":["https://doh.example/dns-query"]},` + outbounds + `}`, nil, "doh.example не найден"},
		"tag taken": {`{"dns":{"servers":["https://8.8.8.8/dns-query"]},"outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443}]}},{"tag":"tun-dns","protocol":"freedom"}]}`,
			nil, `"tun-dns"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(tc.cfg)
			out, skipped, err := HijackTunDNS(raw, func(h string) []string { return tc.lookup[h] })
			if err != nil {
				t.Fatalf("HijackTunDNS: %v", err)
			}
			if !strings.Contains(skipped, tc.reason) {
				t.Errorf("skipped = %q, want it to mention %q", skipped, tc.reason)
			}
			if string(out) != string(raw) {
				t.Errorf("config changed while skipped:\n%s", out)
			}
		})
	}
}

func TestDNSServerHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://dns.google/dns-query":      "dns.google",
		"https://8.8.8.8:443/dns-query":     "8.8.8.8",
		"tcp://1.1.1.1:53":                  "1.1.1.1",
		"quic://dns.adguard-dns.com":        "dns.adguard-dns.com",
		"1.1.1.1":                           "1.1.1.1",
		"dns.example:5353":                  "dns.example",
		"[2606:4700:4700::1111]:53":         "2606:4700:4700::1111",
		"2606:4700:4700::1111":              "2606:4700:4700::1111",
		"https://[2001:4860:4860::8888]/dq": "2001:4860:4860::8888",
	} {
		if got := dnsServerHost(in); got != want {
			t.Errorf("dnsServerHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The hijacked config is one the core accepts, a balancer profile included.
func TestHijackTunDNS_PassesXrayTest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the core checked here is the Linux build")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "xray_linux", "bin", "xray")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("xray binary not present, skipping core validation")
	}
	vless := func(tag, host string) string {
		return `{"tag":"` + tag + `","protocol":"vless","settings":{"vnext":[{"address":"` + host + `","port":443,
			"users":[{"id":"27848739-7e62-4138-9fd3-098a63964b6b","encryption":"none"}]}]},
		 "streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"` + host + `"}}}`
	}
	raw := json.RawMessage(`{
		"dns":{"hosts":{"dns.google":["8.8.8.8"]},
		       "servers":[{"address":"https://dns.google/dns-query","domains":["geosite:category-ru"],"skipFallback":true},
		                  "https://dns.adguard-dns.com/dns-query"],"queryStrategy":"UseIPv4"},
		"inbounds":[{"tag":"tun","protocol":"dokodemo-door","listen":"127.0.0.1","port":1,"settings":{"address":"1.1.1.1","port":53,"network":"tcp,udp"}}],
		"outbounds":[` + vless("eu", "203.0.113.1") + `,` + vless("eu-2", "203.0.113.2") + `,
			{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}],
		"routing":{"domainStrategy":"IPIfNonMatch",
			"balancers":[{"tag":"B","selector":["eu"],"strategy":{"type":"random"}}],
			"rules":[{"type":"field","network":"tcp,udp","balancerTag":"B"}]}
	}`)
	lookup, _ := fakeLookup(map[string][]string{"dns.adguard-dns.com": {"94.140.14.14"}})
	out, skipped, err := HijackTunDNS(raw, lookup)
	if err != nil || skipped != "" {
		t.Fatalf("HijackTunDNS: skipped=%q err=%v", skipped, err)
	}
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgPath, out, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-test", "-c", cfgPath) //nolint:gosec // G204: the repo's own core on a temp config
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Join(root, "xray_linux", "bin"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xray rejected the hijacked config: %v\n%s\n%s", err, b, out)
	}
}

func TestWindowsTunDNS_UsesSyntheticResolver(t *testing.T) {
	for _, tc := range []struct {
		name, dns, wantAction string
	}{
		{"panel DoH", `"https://1.1.1.1/dns-query"`, "hijack"},
		{"system-dependent resolver", `"localhost"`, "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"dns":{"servers":[` + tc.dns + `]},"outbounds":[{"tag":"proxy","protocol":"vless"}]}`)
			out, skipped, err := HijackWindowsTunDNS(raw, func(string) []string { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if (skipped == "") != (tc.wantAction == "hijack") {
				t.Errorf("hijack skipped reason = %q, want action %s", skipped, tc.wantAction)
			}
			var cfg struct {
				Outbounds []struct {
					Tag      string `json:"tag"`
					Settings struct {
						RewriteAddress string `json:"rewriteAddress"`
						Rules          []struct {
							Action string `json:"action"`
						} `json:"rules"`
					} `json:"settings"`
				} `json:"outbounds"`
				Routing struct {
					Rules []struct {
						IP []string `json:"ip"`
					} `json:"rules"`
				} `json:"routing"`
			}
			if err := json.Unmarshal(out, &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Outbounds) != 3 || cfg.Outbounds[0].Tag != "proxy" ||
				cfg.Outbounds[1].Tag != TunDNSOutboundTag {
				t.Fatalf("unexpected outbounds: %s", out)
			}
			s := cfg.Outbounds[1].Settings
			if s.RewriteAddress != "1.1.1.1" || len(s.Rules) == 0 || s.Rules[0].Action != tc.wantAction {
				t.Fatalf("unexpected DNS settings: %+v", s)
			}
			if got := cfg.Routing.Rules[0].IP; len(got) != 1 || got[0] != WindowsTunDNSAddr {
				t.Errorf("DNS rule matches %v, want %s", got, WindowsTunDNSAddr)
			}
		})
	}
}
