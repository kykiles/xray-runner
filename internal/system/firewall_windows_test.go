//go:build windows && (amd64 || arm64)

package system

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// H08: the core's permit is keyed by its App ID, and a connection carries the
// ID of the long name. A core started from an 8.3 path — %TEMP% on a hosted
// runner reads C:\Users\RUNNER~1\… — used to get a permit that matched nothing,
// so its direct traffic hit the block.
func TestWFPAppIDIgnoresShortNames(t *testing.T) {
	long := filepath.Join(t.TempDir(), "long directory name", "xray.exe")
	if err := os.MkdirAll(filepath.Dir(long), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(long, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	short := windows.UTF16ToString(buf[:n])
	if short == long {
		t.Skip("8.3 names are off on this volume")
	}

	blob := func(path string) []byte {
		t.Helper()
		b, err := wfpAppID(path)
		if err != nil {
			t.Fatalf("app ID of %s: %v", path, err)
		}
		defer wfpFree(b)
		return bytes.Clone(unsafe.Slice(b.data, b.size))
	}
	if a, b := blob(long), blob(short); !bytes.Equal(a, b) {
		t.Errorf("app ID of %s differs from that of %s", short, long)
	}
}
