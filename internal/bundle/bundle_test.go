package bundle

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestUnpackDecompresses(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte("core")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	fsys := fstest.MapFS{
		"xray.exe.gz": {Data: gz.Bytes()},
		"geoip.dat":   {Data: []byte("geo")},
	}
	dir := t.TempDir()
	if err := unpack(fsys, dir); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{"xray.exe": "core", "geoip.dat": "geo"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, хотели %q", name, got, want)
		}
	}
}

// The cache directory is keyed by content, so a changed asset must not reuse it.
func TestHashAssetsTracksContent(t *testing.T) {
	a, err := hashAssets(fstest.MapFS{"x": {Data: []byte("1")}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashAssets(fstest.MapFS{"x": {Data: []byte("2")}})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("хеш не изменился при другом содержимом")
	}
}
