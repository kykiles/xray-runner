package subscription

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildHysteria2_ObfsReturnsError(t *testing.T) {
	_, err := BuildOutboundJSON(&SubEntry{
		Protocol:     "hysteria2",
		Address:      "h.example.com",
		Port:         443,
		Password:     "p",
		Obfs:         "salamander",
		ObfsPassword: "x",
	})
	if err == nil {
		t.Fatal("expected error for hysteria2 with obfs, got nil")
	}
	if !strings.Contains(err.Error(), "Salamander") {
		t.Errorf("expected error mentioning Salamander, got: %v", err)
	}
}

func TestBuildHysteria2_NoObfsSucceeds(t *testing.T) {
	raw, err := BuildOutboundJSON(&SubEntry{
		Protocol: "hysteria2",
		Address:  "h.example.com",
		Port:     443,
		Password: "p",
	})
	if err != nil {
		t.Fatalf("BuildOutboundJSON: %v", err)
	}

	var out struct {
		Stream struct {
			HysteriaSettings struct {
				Auth string `json:"auth"`
			} `json:"hysteriaSettings"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, raw)
	}
	if out.Stream.HysteriaSettings.Auth != "p" {
		t.Errorf("auth = %q, want %q", out.Stream.HysteriaSettings.Auth, "p")
	}
	if strings.Contains(out.Stream.HysteriaSettings.Auth, "|") {
		t.Errorf("auth must not contain '|', got: %q", out.Stream.HysteriaSettings.Auth)
	}
}

func TestBuildTrojan_RealitySpiderX(t *testing.T) {
	raw, err := BuildOutboundJSON(&SubEntry{
		Protocol: "trojan", Address: "tr.example.com", Port: 443,
		Password: "sekret", Network: "tcp", Security: "reality",
		SNI: "www.microsoft.com", PublicKey: "PBK", ShortID: "ab12", SpiderX: "/spx",
	})
	if err != nil {
		t.Fatalf("BuildOutboundJSON: %v", err)
	}
	var out struct {
		Protocol string `json:"protocol"`
		Settings struct {
			Servers []struct {
				Address  string `json:"address"`
				Password string `json:"password"`
			} `json:"servers"`
		} `json:"settings"`
		Stream struct {
			Reality struct {
				PublicKey string `json:"publicKey"`
				SpiderX   string `json:"spiderX"`
			} `json:"realitySettings"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, raw)
	}
	if out.Protocol != "trojan" || len(out.Settings.Servers) != 1 ||
		out.Settings.Servers[0].Password != "sekret" || out.Settings.Servers[0].Address != "tr.example.com" {
		t.Errorf("trojan settings mismatch: %s", raw)
	}
	if out.Stream.Reality.PublicKey != "PBK" || out.Stream.Reality.SpiderX != "/spx" {
		t.Errorf("reality/spiderX mismatch: %s", raw)
	}
}

func TestBuildVLESS_TransportSettings(t *testing.T) {
	cases := []struct {
		name    string
		entry   SubEntry
		jsonHas []string
	}{
		{"xhttp", SubEntry{Protocol: "vless", Address: "a", Port: 1, UUID: "u", Network: "xhttp", Path: "/p", XHTTPMode: "stream-one"},
			[]string{`"xhttpSettings"`, `"stream-one"`, `"/p"`}},
		{"httpupgrade", SubEntry{Protocol: "vless", Address: "a", Port: 1, UUID: "u", Network: "httpupgrade", Path: "/hu", Host: "h.example"},
			[]string{`"httpupgradeSettings"`, `"/hu"`, `"h.example"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := BuildOutboundJSON(&c.entry)
			if err != nil {
				t.Fatalf("BuildOutboundJSON: %v", err)
			}
			for _, want := range c.jsonHas {
				if !strings.Contains(string(raw), want) {
					t.Errorf("generated JSON missing %s:\n%s", want, raw)
				}
			}
		})
	}
}

func TestEncodeURL_TrojanRoundTrip(t *testing.T) {
	e := SubEntry{
		Protocol: "trojan", Address: "tr.example.com", Port: 8443,
		Password: "p@ss", Network: "ws", Security: "tls",
		Path: "/ws", Host: "cdn.example.com", SNI: "cdn.example.com",
		Remarks: "🇩🇪 trojan",
	}
	link, err := EncodeURL(&e)
	if err != nil {
		t.Fatalf("EncodeURL: %v", err)
	}
	got, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL(%q): %v", link, err)
	}
	if got.Protocol != "trojan" || got.Password != e.Password || got.Address != e.Address ||
		got.Port != e.Port || got.Network != e.Network || got.Path != e.Path ||
		got.Host != e.Host || got.SNI != e.SNI || got.Remarks != e.Remarks {
		t.Errorf("trojan round-trip mismatch:\n got %+v\nwant %+v", got, e)
	}
}
