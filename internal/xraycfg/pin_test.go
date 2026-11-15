package xraycfg

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func fakeLookup(answers map[string][]string) (func(string) []string, *[]string) {
	var asked []string
	return func(h string) []string {
		asked = append(asked, h)
		return answers[h]
	}, &asked
}

// Once the system's DNS goes into the tunnel, the core must not look its own
// servers up there: every server domain lands in dns.hosts, and the outbounds
// that dial one get a domainStrategy so the core reads its own hosts.
func TestPinServerAddresses_PinsDomainsOnly(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"a","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443}]}},
		{"tag":"b","protocol":"trojan","settings":{"servers":[{"address":"203.0.113.7","port":443}]}},
		{"tag":"h","protocol":"hysteria","settings":{"address":"h.example.com","port":8443}},
		{"tag":"direct","protocol":"freedom"},
		{"tag":"block","protocol":"blackhole"}
	]}`)
	lookup, asked := fakeLookup(map[string][]string{
		"a.example.com": {"198.51.100.1", "198.51.100.2"},
		"h.example.com": {"198.51.100.9"},
	})

	out, pinned, err := PinServerAddresses(raw, lookup)
	if err != nil {
		t.Fatalf("PinServerAddresses: %v", err)
	}
	if want := []string{"a.example.com", "h.example.com"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("looked up %v, want %v (no IP literals, each domain once)", *asked, want)
	}
	if want := map[string][]string{"a.example.com": {"198.51.100.1", "198.51.100.2"}, "h.example.com": {"198.51.100.9"}}; !reflect.DeepEqual(pinned, want) {
		t.Errorf("pinned = %v, want %v", pinned, want)
	}

	var cfg struct {
		DNS struct {
			Hosts map[string][]string `json:"hosts"`
		} `json:"dns"`
		Outbounds []struct {
			Tag    string `json:"tag"`
			Stream *struct {
				Sockopt *struct {
					DomainStrategy string `json:"domainStrategy"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := map[string][]string{"full:a.example.com": {"198.51.100.1", "198.51.100.2"}, "full:h.example.com": {"198.51.100.9"}}; !reflect.DeepEqual(cfg.DNS.Hosts, want) {
		t.Errorf("dns.hosts = %v, want %v", cfg.DNS.Hosts, want)
	}
	for _, o := range cfg.Outbounds {
		got := ""
		if o.Stream != nil && o.Stream.Sockopt != nil {
			got = o.Stream.Sockopt.DomainStrategy
		}
		want := ""
		if o.Tag == "a" || o.Tag == "h" {
			want = "UseIPv4"
		}
		if got != want {
			t.Errorf("%s: domainStrategy = %q, want %q", o.Tag, got, want)
		}
	}
}

// The panel's own choices stay: its hosts entry for a server, its other hosts
// and dns servers, its domainStrategy and the rest of its sockopt.
func TestPinServerAddresses_KeepsPanelSettings(t *testing.T) {
	raw := json.RawMessage(`{
		"dns":{"servers":["1.1.1.1"],"hosts":{"panel.example.com":"192.0.2.1","Full:B.example.com":["192.0.2.2"]}},
		"outbounds":[
			{"tag":"a","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443}]},
			 "streamSettings":{"network":"ws","sockopt":{"tcpFastOpen":true}}},
			{"tag":"b","protocol":"vless","settings":{"vnext":[{"address":"b.example.com","port":443}]},
			 "streamSettings":{"sockopt":{"DomainStrategy":"UseIP"}}}
		]}`)
	lookup, _ := fakeLookup(map[string][]string{"a.example.com": {"198.51.100.1"}, "b.example.com": {"198.51.100.2"}})

	out, _, err := PinServerAddresses(raw, lookup)
	if err != nil {
		t.Fatalf("PinServerAddresses: %v", err)
	}
	var cfg struct {
		DNS struct {
			Servers []string                   `json:"servers"`
			Hosts   map[string]json.RawMessage `json:"hosts"`
		} `json:"dns"`
		Outbounds []struct {
			Stream map[string]json.RawMessage `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(cfg.DNS.Servers, []string{"1.1.1.1"}) {
		t.Errorf("dns.servers = %v, the panel's are gone", cfg.DNS.Servers)
	}
	wantHosts := map[string]string{
		"panel.example.com":  `"192.0.2.1"`,
		"Full:B.example.com": `["192.0.2.2"]`,
		"full:a.example.com": `["198.51.100.1"]`,
	}
	if len(cfg.DNS.Hosts) != len(wantHosts) {
		t.Errorf("dns.hosts = %s, want exactly %v", out, wantHosts)
	}
	for k, v := range wantHosts {
		if string(cfg.DNS.Hosts[k]) != v {
			t.Errorf("dns.hosts[%q] = %s, want %s", k, cfg.DNS.Hosts[k], v)
		}
	}
	if got := string(cfg.Outbounds[0].Stream["sockopt"]); got != `{"domainStrategy":"UseIPv4","tcpFastOpen":true}` {
		t.Errorf("a sockopt = %s", got)
	}
	if got := string(cfg.Outbounds[0].Stream["network"]); got != `"ws"` {
		t.Errorf("a network = %s, the stream settings were replaced", got)
	}
	if got := string(cfg.Outbounds[1].Stream["sockopt"]); got != `{"DomainStrategy":"UseIP"}` {
		t.Errorf("b sockopt = %s, the panel's strategy must stay", got)
	}
}

// Nothing to pin — all IPs, or no answer — leaves the config byte for byte.
func TestPinServerAddresses_NothingToPin(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"a","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443}]}},
		{"tag":"b","protocol":"vless","settings":{"vnext":[{"address":"offline.example.com","port":443}]}}
	]}`)
	lookup, _ := fakeLookup(nil)
	out, pinned, err := PinServerAddresses(raw, lookup)
	if err != nil {
		t.Fatalf("PinServerAddresses: %v", err)
	}
	if pinned != nil || string(out) != string(raw) {
		t.Errorf("changed a config with nothing to pin: pinned=%v\n%s", pinned, out)
	}
}

// The pinned config is one the core accepts.
func TestPinServerAddresses_PassesXrayTest(t *testing.T) {
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
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"a","protocol":"vless","settings":{"vnext":[{"address":"a.example.com","port":443,
			"users":[{"id":"27848739-7e62-4138-9fd3-098a63964b6b","encryption":"none"}]}]},
		 "streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"a.example.com"}}},
		{"tag":"direct","protocol":"freedom"}
	]}`)
	lookup, _ := fakeLookup(map[string][]string{"a.example.com": {"198.51.100.1"}})
	out, _, err := PinServerAddresses(raw, lookup)
	if err != nil {
		t.Fatalf("PinServerAddresses: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(cfgPath, out, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-test", "-c", cfgPath) //nolint:gosec // G204: the repo's own core on a temp config
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xray rejected the pinned config: %v\n%s\n%s", err, b, out)
	}
}
