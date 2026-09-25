package xray

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildAssetXray builds a mock core that appends the XRAY_LOCATION_ASSET it
// was started with, one line per start, to the file named by MOCK_XRAY_SEEN.
func buildAssetXray(t *testing.T) string {
	t.Helper()
	src := `package main

import "os"

func main() {
	f, _ := os.OpenFile(os.Getenv("MOCK_XRAY_SEEN"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(os.Getenv("XRAY_LOCATION_ASSET") + "\n")
	_ = f.Close()
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, filepath.Join(dir, "main.go")).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, b)
	}
	return out
}

// seenAssets runs the config test and one start, and returns the asset
// directory each saw.
func seenAssets(t *testing.T, r *Runner, seen string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.TestConfig(ctx); err != nil {
		t.Fatalf("TestConfig: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = r.Wait()
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A runner given an asset directory hands it to both the config test and the
// core it starts, over whatever the process's own environment says.
func TestRunner_SetAssetDir(t *testing.T) {
	binary := buildAssetXray(t)
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("MOCK_XRAY_SEEN", seen)
	t.Setenv("XRAY_LOCATION_ASSET", "/inherited")

	r := New(binary, []byte("{}"))
	r.SetAssetDir("/session/geo")
	got := seenAssets(t, r, seen)
	if len(got) != 2 || got[0] != "/session/geo" || got[1] != "/session/geo" {
		t.Errorf("cores saw asset dirs %q, want /session/geo twice", got)
	}
}

// Without one, the core inherits the process's.
func TestRunner_InheritsAssetDir(t *testing.T) {
	binary := buildAssetXray(t)
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("MOCK_XRAY_SEEN", seen)
	t.Setenv("XRAY_LOCATION_ASSET", "/inherited")

	got := seenAssets(t, New(binary, []byte("{}")), seen)
	if len(got) != 2 || got[0] != "/inherited" || got[1] != "/inherited" {
		t.Errorf("cores saw asset dirs %q, want /inherited twice", got)
	}
}
