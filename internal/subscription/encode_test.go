package subscription

import "testing"

func TestEncodeURL_VLESSRealityRoundTrip(t *testing.T) {
	e := SubEntry{
		Protocol: "vless", Address: "vless.example.com", Port: 443,
		UUID: "11111111-2222-3333-4444-555555555555",
		Flow: "xtls-rprx-vision", Network: "tcp", Security: "reality",
		SNI: "www.microsoft.com", Fingerprint: "chrome",
		PublicKey: "abcdefPUBKEY", ShortID: "0123abcd",
		Remarks: "🇳🇱 NL node",
	}
	link, err := EncodeURL(&e)
	if err != nil {
		t.Fatalf("EncodeURL: %v", err)
	}
	got, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL(%q): %v", link, err)
	}
	if got.Protocol != "vless" || got.Address != e.Address || got.Port != e.Port {
		t.Errorf("base mismatch: %+v", got)
	}
	if got.UUID != e.UUID || got.Flow != e.Flow || got.Security != e.Security {
		t.Errorf("auth/security mismatch: %+v", got)
	}
	if got.SNI != e.SNI || got.PublicKey != e.PublicKey || got.ShortID != e.ShortID || got.Fingerprint != e.Fingerprint {
		t.Errorf("reality mismatch: %+v", got)
	}
	if got.Remarks != e.Remarks {
		t.Errorf("Remarks = %q, want %q", got.Remarks, e.Remarks)
	}
}

func TestEncodeURL_SSRoundTrip(t *testing.T) {
	e := SubEntry{
		Protocol: "ss", Address: "ss.example.com", Port: 8443,
		Method: "aes-256-gcm", Password: "secret", Remarks: "ss node",
	}
	link, err := EncodeURL(&e)
	if err != nil {
		t.Fatalf("EncodeURL: %v", err)
	}
	got, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL(%q): %v", link, err)
	}
	if got.Method != e.Method || got.Password != e.Password ||
		got.Address != e.Address || got.Port != e.Port || got.Remarks != e.Remarks {
		t.Errorf("ss round-trip mismatch: %+v", got)
	}
}

func TestEncodeURL_VMessRoundTrip(t *testing.T) {
	e := SubEntry{
		Protocol: "vmess", Address: "vm.example.com", Port: 443,
		UUID: "aaaa-bbbb", Network: "ws", Path: "/ws", Host: "vm.example.com",
		Security: "tls", Remarks: "vmess node",
	}
	link, err := EncodeURL(&e)
	if err != nil {
		t.Fatalf("EncodeURL: %v", err)
	}
	got, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL(%q): %v", link, err)
	}
	if got.Protocol != "vmess" || got.Address != e.Address || got.Port != e.Port ||
		got.UUID != e.UUID || got.Network != e.Network || got.Path != e.Path ||
		got.Host != e.Host || got.Security != e.Security {
		t.Errorf("vmess round-trip mismatch: %+v", got)
	}
}

func TestEncodeURL_Hysteria2RoundTrip(t *testing.T) {
	e := SubEntry{
		Protocol: "hysteria2", Address: "h.example.com", Port: 443,
		Password: "pass", SNI: "h.example.com", Insecure: true,
		Remarks: "hy2 node",
	}
	link, err := EncodeURL(&e)
	if err != nil {
		t.Fatalf("EncodeURL: %v", err)
	}
	got, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL(%q): %v", link, err)
	}
	if got.Protocol != "hysteria2" || got.Address != e.Address || got.Port != e.Port ||
		got.Password != e.Password || got.SNI != e.SNI || got.Insecure != e.Insecure ||
		got.Remarks != e.Remarks {
		t.Errorf("hysteria2 round-trip mismatch: %+v", got)
	}
}

func TestEncodeURL_Unsupported(t *testing.T) {
	_, err := EncodeURL(&SubEntry{Protocol: "socks"})
	if err == nil {
		t.Fatal("expected error for unsupported protocol, got nil")
	}
}
