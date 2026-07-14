package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/subscription"
)

func TestKeysFilePath_DomainUppercased(t *testing.T) {
	got, err := keysFilePath("https://glowshine.tech/sub/abc?token=1")
	if err != nil {
		t.Fatalf("keysFilePath: %v", err)
	}
	want := filepath.Join("keys", "GLOWSHINE.TECH.md")
	if got != want {
		t.Errorf("keysFilePath = %q, want %q", got, want)
	}
}

func TestKeysFilePath_StripsPort(t *testing.T) {
	got, err := keysFilePath("https://sub.example.com:8443/x")
	if err != nil {
		t.Fatalf("keysFilePath: %v", err)
	}
	want := filepath.Join("keys", "SUB.EXAMPLE.COM.md")
	if got != want {
		t.Errorf("keysFilePath = %q, want %q", got, want)
	}
}

func TestKeysFilePath_EmptyHost(t *testing.T) {
	if _, err := keysFilePath("not-a-url"); err == nil {
		t.Fatal("expected error for URL without host, got nil")
	}
}

func TestRenderMarkdown_GroupsByProtocol(t *testing.T) {
	entries := []subscription.SubEntry{
		{Protocol: "vless", Address: "a.example.com", Port: 443, UUID: "u1", Network: "tcp", Remarks: "NL"},
		{Protocol: "ss", Address: "b.example.com", Port: 8443, Method: "aes-256-gcm", Password: "p", Remarks: "SS2022"},
		{Protocol: "vless", Address: "c.example.com", Port: 443, UUID: "u2", Network: "tcp", Remarks: "DE"},
	}
	md := renderMarkdown("https://glowshine.tech/sub", entries, time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC))

	if !strings.Contains(md, "## VLESS") || !strings.Contains(md, "## SS") {
		t.Errorf("missing protocol sections:\n%s", md)
	}
	if !strings.Contains(md, "NL") || !strings.Contains(md, "SS2022") || !strings.Contains(md, "DE") {
		t.Errorf("missing remarks labels:\n%s", md)
	}
	if !strings.Contains(md, "vless://u1@a.example.com:443") {
		t.Errorf("missing vless link:\n%s", md)
	}
	// Оба vless-узла в одной секции: "## SS" встречается после "## VLESS".
	if strings.Index(md, "## VLESS") > strings.Index(md, "## SS") {
		t.Errorf("VLESS section should come before SS:\n%s", md)
	}
}
