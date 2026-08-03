package app

import (
	"os"
	"path/filepath"
	"testing"

	"xray-runner/internal/subscription"
)

// Saving lands under configs/<subscription>/<name>.json, recreates the directory
// if it is gone, and overwrites an earlier copy of the same server.
func TestSaveConfig(t *testing.T) {
	isolateState(t)

	a := &App{}
	a.nav.subs = []subscription.NamedSubscription{{URL: "https://panel.example.com/sub/uuid", Name: "panel.example.com"}}

	path, err := a.saveConfig("DE · Frankfurt", `{"a":1}`)
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	want := filepath.Join("configs", "panel.example.com", "DE___Frankfurt.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	os.RemoveAll("configs")
	if _, err := a.saveConfig("DE · Frankfurt", `{"a":2}`); err != nil {
		t.Fatalf("saveConfig after the directory was deleted: %v", err)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "{\"a\":2}\n" {
		t.Errorf("content = %q, want the newer version", data)
	}
}

// A name full of separators must stay one path element.
func TestSafeName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"../../etc/passwd", "etc_passwd"},
		{"Нидерланды 🇳🇱", "Нидерланды"},
		{"", "config"},
		{"///", "config"},
	} {
		if got := safeName(c.in); got != c.want {
			t.Errorf("safeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
