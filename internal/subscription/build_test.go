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
