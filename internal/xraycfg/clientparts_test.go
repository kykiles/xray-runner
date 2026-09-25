package xraycfg

import (
	"encoding/json"
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
