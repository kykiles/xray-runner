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

func TestSwapReplacesWithoutBackup(t *testing.T) {
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
	if _, err := os.Stat(dst + ".bak"); !os.IsNotExist(err) {
		t.Errorf("swap left a .bak behind: stat err = %v", err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("swap left the .new source behind: stat err = %v", err)
	}
}

func TestFinalizeFileSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(path, []byte("DB"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeFile(path, 0o644); err != nil {
		t.Fatalf("finalizeFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", fi.Mode().Perm())
	}
}

func TestTrimToLatestStable(t *testing.T) {
	pre := func(tag string) Release { return Release{Tag: tag, Prerelease: true} }
	rel := func(tag string) Release { return Release{Tag: tag} }

	tests := []struct {
		name string
		in   []Release
		want []string // expected tags
	}{
		{"stable first", []Release{rel("v1.9"), rel("v1.8"), pre("v1.7-pre")}, []string{"v1.9"}},
		{"stable in middle", []Release{pre("v1.9-pre"), pre("v1.8-pre"), rel("v1.7"), pre("v1.6-pre")}, []string{"v1.9-pre", "v1.8-pre", "v1.7"}},
		{"no stable", []Release{pre("v1.9-pre"), pre("v1.8-pre")}, []string{"v1.9-pre", "v1.8-pre"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimToLatestStable(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d (%v), want %d (%v)", len(got), tags(got), len(tt.want), tt.want)
			}
			for i, r := range got {
				if r.Tag != tt.want[i] {
					t.Errorf("tag[%d] = %q, want %q", i, r.Tag, tt.want[i])
				}
			}
		})
	}
}

func tags(rels []Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Tag
	}
	return out
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
