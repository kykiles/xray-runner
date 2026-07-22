package subscription

import (
	"encoding/base64"
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

// A JSON subscription may spell the protocol "hysteria" rather than "hy2";
// everything downstream keys off the normalized name.
func TestParseXrayJSON_NormalizesHysteria(t *testing.T) {
	e := parseXrayJSON(map[string]interface{}{
		"protocol": "hysteria", "address": "a.example.com",
		"port": float64(443), "password": "x",
	})
	if e.Protocol != "hysteria2" {
		t.Fatalf("Protocol = %q, want hysteria2", e.Protocol)
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

// vmessLink builds a vmess:// share link from the standard field map.
func vmessLink(t *testing.T, fields map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal vmess fields: %v", err)
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

// In a vmess share link "scy" carries the encryption and "type" carries the
// header obfuscation. Reading "type" as the encryption produces an outbound
// xray rejects outright (security:"http").
func TestParseURL_VMessEncryptionComesFromScy(t *testing.T) {
	link := vmessLink(t, map[string]interface{}{
		"v": "2", "ps": "node", "add": "1.2.3.4", "port": "443",
		"id":  "b831381d-6324-4d53-ad4f-8cda48b30811",
		"aid": "0", "scy": "aes-128-gcm", "net": "tcp", "type": "http",
	})

	e, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.Encryption != "aes-128-gcm" {
		t.Errorf("Encryption = %q, want aes-128-gcm", e.Encryption)
	}
}

// Panels that omit "scy" but write "security" should still be honored.
func TestParseURL_VMessEncryptionFallsBackToSecurity(t *testing.T) {
	link := vmessLink(t, map[string]interface{}{
		"add": "1.2.3.4", "port": "443",
		"id":  "b831381d-6324-4d53-ad4f-8cda48b30811",
		"net": "tcp", "security": "chacha20-poly1305",
	})

	e, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.Encryption != "chacha20-poly1305" {
		t.Errorf("Encryption = %q, want chacha20-poly1305", e.Encryption)
	}
}

// A CDN vmess link points "add" at an IP and carries the real hostname in
// "sni"; losing it makes the TLS handshake use the address and fail.
func TestParseURL_VMessKeepsSNIAndFingerprint(t *testing.T) {
	link := vmessLink(t, map[string]interface{}{
		"add": "1.2.3.4", "port": "443",
		"id":  "b831381d-6324-4d53-ad4f-8cda48b30811",
		"net": "ws", "tls": "tls", "sni": "cdn.example.com", "fp": "chrome",
	})

	e, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.SNI != "cdn.example.com" {
		t.Errorf("SNI = %q, want cdn.example.com", e.SNI)
	}
	if e.Fingerprint != "chrome" {
		t.Errorf("Fingerprint = %q, want chrome", e.Fingerprint)
	}
}

// SIP002 allows the userinfo to be plain "method:password" as well as base64.
// Rejecting the plain form drops the server from the list entirely.
func TestParseURL_SSPlainUserinfo(t *testing.T) {
	e, err := parseURL("ss://aes-256-gcm:secret@ss.example.com:8443#plain")
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	if e.Method != "aes-256-gcm" {
		t.Errorf("Method = %q, want aes-256-gcm", e.Method)
	}
	if e.Password != "secret" {
		t.Errorf("Password = %q, want secret", e.Password)
	}
}
