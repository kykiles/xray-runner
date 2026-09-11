package subscription

import (
	"encoding/json"
	"strings"
	"testing"

	"xray-runner/internal/xraycfg"
)

// A07 for VMess: the raw e.Security used to go straight into streamSettings,
// so "Reality" built an outbound with no reality block and "xtls" one with a
// security name and no settings at all.
func TestBuildVMess_SecurityNormalized(t *testing.T) {
	cases := []struct {
		security string
		want     string // "" = no security block; "error" = refused
	}{
		{"Reality", "reality"},
		{"REALITY", "reality"},
		{" reality ", "reality"},
		{"TLS", "tls"},
		{"", ""},
		{"none", ""},
		{"xtls", "error"},
		{"tls1.3", "error"},
		{"foo", "error"},
	}
	for _, c := range cases {
		t.Run(c.security, func(t *testing.T) {
			e := &SubEntry{
				Protocol: "vmess", Address: "example.com", Port: 443,
				UUID:     "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				Security: c.security, SNI: "a.com", PublicKey: "PUBKEY", ShortID: "ab12",
			}
			raw, err := BuildOutboundJSON(e)
			if c.want == "error" {
				if err == nil {
					t.Fatalf("security=%q built %s, want an error", c.security, raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("security=%q: %v", c.security, err)
			}
			var ob xraycfg.VMessOutbound
			if err := json.Unmarshal(raw, &ob); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			ss := ob.Stream
			if ss.Security != c.want {
				t.Errorf("security=%q → %q, want %q", c.security, ss.Security, c.want)
			}
			switch c.want {
			case "reality":
				r := ss.Reality
				if r == nil || r.PublicKey != "PUBKEY" || r.ShortID != "ab12" || r.ServerName != "a.com" {
					t.Errorf("security=%q: realitySettings = %+v, want pbk/sid/sni kept", c.security, r)
				}
			case "tls":
				if ss.TLSSettings == nil {
					t.Errorf("security=%q: no tlsSettings", c.security)
				}
			case "":
				if ss.Reality != nil || ss.TLSSettings != nil {
					t.Errorf("security=%q: got a security block, want none", c.security)
				}
			}
		})
	}
}

// The list marks an entry Validate refuses and will not connect to it, so an
// unsupported security has to be refused there — with the value in the message.
func TestValidate_UnsupportedSecurity(t *testing.T) {
	base := map[string]SubEntry{
		"vless":  {Protocol: "vless", UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		"vmess":  {Protocol: "vmess", UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		"trojan": {Protocol: "trojan", Password: "secret"},
	}
	for proto, e := range base {
		e.Address, e.Port = "example.com", 443
		for _, sec := range []string{"xtls", "tls1.3", "foo"} {
			e.Security = sec
			err := e.Validate()
			if err == nil || !strings.Contains(err.Error(), "не поддерживается: security="+sec) {
				t.Errorf("%s security=%q: Validate() = %v, want a refusal naming the value", proto, sec, err)
			}
		}
		for _, sec := range []string{"", "none", "Reality", " TLS "} {
			e.Security = sec
			if err := e.Validate(); err != nil {
				t.Errorf("%s security=%q: Validate() = %v, want nil", proto, sec, err)
			}
		}
	}
}
