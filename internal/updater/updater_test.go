package updater

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCoreAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "Xray-linux-64.zip"},
		{"linux", "386", "Xray-linux-32.zip"},
		{"linux", "arm64", "Xray-linux-arm64-v8a.zip"},
		{"darwin", "amd64", "Xray-macos-64.zip"},
		{"darwin", "arm64", "Xray-macos-arm64-v8a.zip"},
		{"windows", "amd64", "Xray-windows-64.zip"},
		{"plan9", "amd64", ""},
		{"linux", "riscv64", ""},
	}
	for _, tt := range tests {
		if got := coreAssetName(tt.goos, tt.goarch); got != tt.want {
			t.Errorf("coreAssetName(%q,%q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestCoreAssetPicksCurrentPlatform(t *testing.T) {
	want := coreAssetName(runtime.GOOS, runtime.GOARCH)
	if want == "" {
		t.Skip("platform not mapped")
	}
	r := Release{Assets: []Asset{
		{Name: "Xray-otheros-64.zip", URL: "x"},
		{Name: want, URL: "right"},
		{Name: geoipName, URL: "g"},
	}}
	a, ok := CoreAsset(r)
	if !ok || a.URL != "right" {
		t.Fatalf("CoreAsset = %+v, ok=%v; want the %q asset", a, ok, want)
	}
}

func TestGeoAssets(t *testing.T) {
	r := Release{Assets: []Asset{
		{Name: geoipName, URL: "ip"},
		{Name: geositeName, URL: "site"},
		{Name: "Xray-linux-64.zip", URL: "core"},
	}}
	ip, site, ok := GeoAssets(r)
	if !ok || ip.URL != "ip" || site.URL != "site" {
		t.Fatalf("GeoAssets = %q,%q,%v", ip.URL, site.URL, ok)
	}

	if _, _, ok := GeoAssets(Release{Assets: []Asset{{Name: geoipName, URL: "ip"}}}); ok {
		t.Error("GeoAssets ok with geosite missing")
	}
}

func TestExtractZipFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "core.zip")
	writeZip(t, zipPath, map[string]string{
		"README.md": "docs",
		"xray":      "BINARY",
	})

	dst := filepath.Join(dir, "xray.new")
	if err := extractZipFile(zipPath, "xray", dst); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "BINARY" {
		t.Errorf("extracted %q, want BINARY", got)
	}

	if err := extractZipFile(zipPath, "missing", filepath.Join(dir, "x")); err == nil {
		t.Error("extract of a missing entry should error")
	}
}

func TestSwapKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "xray")
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	newPath := dst + ".new"
	if err := os.WriteFile(newPath, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swap(newPath, dst); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "NEW" {
		t.Errorf("dst = %q, want NEW", got)
	}
	if got, _ := os.ReadFile(dst + ".bak"); string(got) != "OLD" {
		t.Errorf("backup = %q, want OLD", got)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
