package subscription

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestParseURL_SSValid(t *testing.T) {
	e, err := parseURL("ss://YWVzLTI1Ni1nY206c2VjcmV0@ss.example.com:8443#myserver")
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.Protocol != "ss" {
		t.Errorf("Protocol = %q, want ss", e.Protocol)
	}
	if e.Address != "ss.example.com" {
		t.Errorf("Address = %q, want ss.example.com", e.Address)
	}
	if e.Port != 8443 {
		t.Errorf("Port = %d, want 8443", e.Port)
	}
	if e.Method != "aes-256-gcm" {
		t.Errorf("Method = %q, want aes-256-gcm", e.Method)
	}
	if e.Password != "secret" {
		t.Errorf("Password = %q, want secret", e.Password)
	}
	if e.Remarks != "myserver" {
		t.Errorf("Remarks = %q, want myserver", e.Remarks)
	}
}

func TestParseURL_SSInvalidBase64(t *testing.T) {
	_, err := parseURL("ss://!!!invalid-base64!!!@ss.example.com:8443")
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
	if !strings.Contains(err.Error(), "decode ss base64 userinfo") {
		t.Errorf("expected error about base64 decode, got: %v", err)
	}
}

func TestParseURL_Hy2AliasNormalizedToHysteria2(t *testing.T) {
	e, err := parseURL("hy2://pass@h.example.com:443?sni=h.example.com#hy2server")
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.Protocol != "hysteria2" {
		t.Errorf("Protocol = %q, want hysteria2", e.Protocol)
	}
	if e.Address != "h.example.com" {
		t.Errorf("Address = %q, want h.example.com", e.Address)
	}
	if e.Port != 443 {
		t.Errorf("Port = %d, want 443", e.Port)
	}
	if e.Password != "pass" {
		t.Errorf("Password = %q, want pass", e.Password)
	}
	if e.Remarks != "hy2server" {
		t.Errorf("Remarks = %q, want hy2server", e.Remarks)
	}
}

func TestParseURL_SSBadUserinfoFormat(t *testing.T) {
	// "dGVzdA==" decodes to "test" — no colon, so not method:password
	_, err := parseURL("ss://dGVzdA==@ss.example.com:8443")
	if err == nil {
		t.Fatal("expected error for bad userinfo format, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ss userinfo format") {
		t.Errorf("expected error about userinfo format, got: %v", err)
	}
}

func TestParseXrayJSON_ShortIDAliases(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"shortId", "shortId"},
		{"sid", "sid"},
		{"shortID", "shortID"},
		{"short_id", "short_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"protocol":"vless","address":"x","port":443,"uuid":"u","security":"reality","publicKey":"pk","` + tc.key + `":"abc123"}`)
			var obj map[string]interface{}
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			e := parseXrayJSON(obj)
			if e.ShortID != "abc123" {
				t.Errorf("ShortID = %q, want %q (key=%s)", e.ShortID, "abc123", tc.key)
			}
		})
	}
}

func TestParseXrayJSON_ShortIDStandardPreferredOverAlias(t *testing.T) {
	raw := []byte(`{"protocol":"vless","address":"x","port":443,"shortId":"primary","sid":"fallback"}`)
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := parseXrayJSON(obj)
	if e.ShortID != "primary" {
		t.Errorf("ShortID = %q, want %q (first declared alias should win)", e.ShortID, "primary")
	}
}

func TestParseVlessURL_ShortIDQueryAliases(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"sid", "sid=abc123"},
		{"shortId", "shortId=abc123"},
		{"shortID", "shortID=abc123"},
		{"short_id", "short_id=abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "vless://u@example.com:443?" + tc.query
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			e := SubEntry{}
			parseVlessURL(u, &e)
			if e.ShortID != "abc123" {
				t.Errorf("ShortID = %q, want %q (key=%s)", e.ShortID, "abc123", tc.query)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Errorf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}
