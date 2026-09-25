package xraycfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vlessOutbound = `{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"vpn.example.com","port":443,"users":[{"id":"u"}]}]},
 "streamSettings":{"network":"ws","security":"tls","wsSettings":{"path":"/ws"},"tlsSettings":{"serverName":"vpn.example.com"}}}`

func parts(outbounds, routing string) ClientParts {
	p := ClientParts{Outbounds: json.RawMessage(outbounds)}
	if routing != "" {
		p.Routing = json.RawMessage(routing)
	}
	return p
}

func TestValidateAcceptsProfile(t *testing.T) {
	p := parts(`[`+vlessOutbound+`,{"tag":"direct","protocol":"freedom"},{"tag":"block","protocol":"blackhole"}]`,
		`{"rules":[{"domain":["geosite:category-ru"],"outboundTag":"direct"},{"ip":["geoip:private"],"outboundTag":"direct"}],
		  "balancers":[{"tag":"b","selector":["proxy"]}]}`)
	p.DNS = json.RawMessage(`{"servers":["https://1.1.1.1/dns-query","localhost",{"address":"8.8.8.8","domains":["geosite:google"]}]}`)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// What would have the core read or write a file, or reach a local unix socket,
// with the service's rights is refused.
func TestValidateRefuses(t *testing.T) {
	cases := map[string]ClientParts{
		"no outbounds":   parts(`[]`, ""),
		"unknown proto":  parts(`[{"protocol":"vless-next"}]`, ""),
		"cert file":      parts(`[{"protocol":"trojan","streamSettings":{"tlsSettings":{"certificates":[{"certificateFile":"/etc/shadow"}]}}}]`, ""),
		"key file":       parts(`[{"protocol":"trojan","streamSettings":{"tlsSettings":{"certificates":[{"keyFile":"/x"}]}}}]`, ""),
		"key log":        parts(`[{"protocol":"vless","streamSettings":{"tlsSettings":{"masterKeyLog":"/etc/cron.d/x"}}}]`, ""),
		"reality keylog": parts(`[{"protocol":"vless","streamSettings":{"realitySettings":{"masterKeyLog":"x"}}}]`, ""),
		"unix address":   parts(`[{"protocol":"vless","settings":{"vnext":[{"address":"/run/docker.sock","port":0}]}}]`, ""),
		"abstract sock":  parts(`[{"protocol":"socks","settings":{"servers":[{"address":"@x","port":1}]}}]`, ""),
		"freedom unix":   parts(`[{"protocol":"freedom","settings":{"redirect":"/tmp/s"}}]`, ""),
		"windows path":   parts(`[{"protocol":"vless","settings":{"vnext":[{"address":"C:\\\\ProgramData\\\\x.sock","port":0}]}}]`, ""),
		"windows slash":  parts(`[{"protocol":"vless","settings":{"vnext":[{"address":"c:/x.sock","port":0}]}}]`, ""),
		"domainsocket":   parts(`[{"protocol":"vless","streamSettings":{"network":"domainsocket"}}]`, ""),
		"ext list":       parts(`[{"protocol":"freedom"}]`, `{"rules":[{"domain":["ext:../../etc/passwd:x"],"outboundTag":"direct"}]}`),
		"ext-ip list":    parts(`[{"protocol":"freedom"}]`, `{"rules":[{"ip":["EXT-IP:a.dat:b"],"outboundTag":"direct"}]}`),
	}
	for name, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dns := parts(`[{"protocol":"freedom"}]`, "")
	dns.DNS = json.RawMessage(`{"servers":[{"address":"/run/unix","port":53}]}`)
	if dns.Validate() == nil {
		t.Error("dns unix address: accepted")
	}
}

// The request carries the pieces, never the rest: log files, the api and
// inbounds of the client's config do not survive the trip.
func TestExtractAndAssemble(t *testing.T) {
	full := `{"log":{"access":"/etc/passwd","loglevel":"debug"},"api":{"tag":"api","services":["HandlerService"]},
	  "inbounds":[{"port":1,"protocol":"dokodemo-door"}],"stats":{},"outbounds":[` + vlessOutbound + `],
	  "routing":{"rules":[]},"reverse":{"bridges":[]}}`
	p, err := ExtractClientParts(json.RawMessage(full))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := AssembleService(p, []Inbound{BuildProbeInbound(12345)}, "warning")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"api", "stats", "reverse"} {
		if _, ok := cfg[k]; ok {
			t.Errorf("%s survived", k)
		}
	}
	if string(cfg["log"]) != `{"loglevel":"warning"}` {
		t.Errorf("log = %s", cfg["log"])
	}
	if strings.Contains(string(cfg["inbounds"]), "dokodemo") || !strings.Contains(string(cfg["inbounds"]), "12345") {
		t.Errorf("inbounds = %s", cfg["inbounds"])
	}
	if _, err := AssembleService(p, nil, "trace"); err == nil {
		t.Error("unknown log level accepted")
	}
}

func TestAssembleKeepsWireguardInUserspace(t *testing.T) {
	p := parts(`[{"protocol":"wireguard","settings":{"secretKey":"k","noKernelTun":false,"peers":[{"endpoint":"wg.example.com:51820"}]}}]`, "")
	raw, err := AssembleService(p, nil, "none")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"noKernelTun":true`) {
		t.Fatalf("wireguard not kept in userspace: %s", raw)
	}
}

func TestOutboundServers(t *testing.T) {
	raw := json.RawMessage(`[` + vlessOutbound + `,
	  {"protocol":"trojan","settings":{"servers":[{"address":"t.example.com","port":443}]}},
	  {"protocol":"hysteria2","settings":{"address":"h.example.com","port":8443}},
	  {"protocol":"wireguard","settings":{"peers":[{"endpoint":"[2001:db8::1]:51820"}]}},
	  {"protocol":"freedom","settings":{"redirect":"1.2.3.4:5"}},
	  ` + vlessOutbound + `]`)
	got := OutboundServers(raw)
	want := []Server{
		{"vless", "vpn.example.com", 443}, {"trojan", "t.example.com", 443},
		{"hysteria2", "h.example.com", 8443}, {"wireguard", "2001:db8::1", 51820},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// The core decodes with encoding/json, which takes a key for a field whatever
// its case and with Unicode folded: each spelling below reaches the field the
// check is for, so each is refused.
func TestValidateRefusesFoldedKeys(t *testing.T) {
	// U+212A is the Kelvin sign (a k to the core), ſ the long s (an s).
	cases := map[string]string{
		"Address":          `[{"protocol":"vless","settings":{"vnext":[{"Address":"/run/dbus/system_bus_socket","port":0}]}}]`,
		"ADDRESS abstract": `[{"protocol":"socks","settings":{"servers":[{"ADDRESS":"@x","port":1}]}}]`,
		"addreſs":          `[{"protocol":"vless","settings":{"vnext":[{"addreſs":"/run/x","port":0}]}}]`,
		"escaped Address":  `[{"protocol":"vless","settings":{"vnext":[{"\u0041ddress":"/run/x","port":0}]}}]`,
		"Redirect":         `[{"protocol":"freedom","settings":{"Redirect":"/tmp/s"}}]`,
		"rEdIrEcT windows": `[{"protocol":"freedom","settings":{"rEdIrEcT":"C:\\x.sock"}}]`,
		"Dest":             `[{"protocol":"freedom","settings":{"fallbacks":[{"Dest":"/run/s"}]}}]`,
		"Server":           `[{"protocol":"freedom","settings":{"Server":"@s"}}]`,
		"ſerver":           `[{"protocol":"freedom","settings":{"ſerver":"@s"}}]`,
		"Network":          `[{"protocol":"vless","streamSettings":{"Network":"domainsocket"}}]`,
		"DomainSocket":     `[{"protocol":"vless","streamSettings":{"network":"DomainSocket"}}]`,
		"DS":               `[{"protocol":"vless","streamSettings":{"network":"DS"}}]`,
		"padded value":     `[{"protocol":"vless","settings":{"vnext":[{"address":" /run/x","port":0}]}}]`,
		"CertificateFILE":  `[{"protocol":"trojan","streamSettings":{"tlsSettings":{"certificates":[{"CertificateFILE":"/x"}]}}}]`,
		"Protocol":         `[{"Protocol":"vless-next"}]`,
		"Kelvin twin":      `[{"protocol":"wireguard","settings":{"noKernelTun":true,"no` + "\u212A" + `ernelTun":false}}]`,
		"Kelvin key":       `[{"protocol":"freedom","settings":{"` + "\u212A" + `eep":"x"}}]`,
		"dup exact":        `[{"protocol":"vless","streamSettings":{"network":"ws"},"streamSettings":{"security":"none"}}]`,
		"dup case":         `[{"protocol":"vless","streamSettings":{"network":"ws"},"StreamSettings":{"security":"none"}}]`,
		"dup protocol":     `[{"protocol":"vless","Protocol":"wireguard"}]`,
		"dup noKernelTun":  `[{"protocol":"wireguard","settings":{"noKernelTun":true,"NOKERNELTUN":false}}]`,
		"sockopt mark":     `[{"protocol":"freedom","streamSettings":{"sockopt":{"mark":255}}}]`,
		"sockopt Mark":     `[{"protocol":"freedom","streamSettings":{"SockOpt":{"Mark":255}}}]`,
		"sockopt custom":   `[{"protocol":"freedom","streamSettings":{"sockopt":{"customSockopt":[{"level":"1","opt":"36","value":"1"}]}}}]`,
		"sockopt tproxy":   `[{"protocol":"freedom","streamSettings":{"sockopt":{"tproxy":"tproxy"}}}]`,
		"sockopt unknown":  `[{"protocol":"freedom","streamSettings":{"sockopt":{"somethingNew":1}}}]`,
		"deep":             `[{"protocol":"freedom","settings":` + strings.Repeat(`[`, 100) + strings.Repeat(`]`, 100) + `}]`,
		"trailing garbage": `[{"protocol":"freedom"}] []`,
	}
	for name, outbounds := range cases {
		if err := parts(outbounds, "").Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	routing := parts(`[{"protocol":"freedom"}]`, `{"rules":[{"domain":["geosite:x"],"outboundTag":"direct"}],"RULES":[]}`)
	if routing.Validate() == nil {
		t.Error("routing dup: accepted")
	}
	dns := parts(`[{"protocol":"freedom"}]`, "")
	dns.DNS = json.RawMessage(`{"servers":[{"aDDress":"/run/unix","port":53}]}`)
	if dns.Validate() == nil {
		t.Error("dns Address: accepted")
	}
}

// What a panel puts in sockopt, and the adapter it pins with interface, pass.
func TestValidateAcceptsSockopt(t *testing.T) {
	p := parts(`[{"protocol":"freedom","streamSettings":{"sockopt":{"tcpFastOpen":true,"domainStrategy":"UseIP",
	  "tcpKeepAliveIdle":300,"tcpCongestion":"bbr","interface":"eth0","TCPMptcp":true,
	  "happyEyeballs":{"tryDelayMs":250,"prioritizeIPv6":false}}}},
	  {"tag":"hop","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,"users":[{"id":"u"}]}]},
	  "streamSettings":{"sockopt":{"dialerProxy":"direct"}}}]`, "")
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A full profile the program builds itself — the template around each outbound
// the builders make — goes through the service's check.
func TestValidateAcceptsBuiltProfiles(t *testing.T) {
	tpl, err := LoadTemplate("template.json")
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("fixtures: %v", err)
	}
	for _, f := range fixtures {
		ob, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(MergeConfig(tpl, ob))
		if err != nil {
			t.Fatal(err)
		}
		p, err := ExtractClientParts(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// A twin of noKernelTun that sorts after it would have the last word with the
// core; every spelling goes before the service puts its own.
func TestAssembleDropsNoKernelTunTwins(t *testing.T) {
	for _, settings := range []string{
		`{"secretKey":"k","NoKernelTun":false}`,
		`{"secretKey":"k","nokerneltun":false}`,
	} {
		p := parts(`[{"protocol":"wireguard","settings":`+settings+`}]`, "")
		raw, err := AssembleService(p, nil, "none")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Outbounds []struct {
				Settings struct {
					NoKernelTun bool `json:"noKernelTun"`
					SecretKey   string
				} `json:"settings"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if s := cfg.Outbounds[0].Settings; !s.NoKernelTun || s.SecretKey != "k" || strings.Count(strings.ToLower(string(raw)), "nokerneltun") != 1 {
			t.Errorf("%s: %s", settings, raw)
		}
	}
	// Settings under another case are the ones the core reads.
	p := parts(`[{"protocol":"wireguard","Settings":{"secretKey":"k","noKernelTun":false}}]`, "")
	raw, err := AssembleService(p, nil, "none")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Settings":{"noKernelTun":true,"secretKey":"k"}`) || strings.Contains(string(raw), `"settings"`) {
		t.Errorf("Settings: %s", raw)
	}
}
