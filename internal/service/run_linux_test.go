//go:build linux

package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenerUID(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2A3A 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11 1 0 100 0 0 10 0
   1: 0100007F:2A3A 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1001        0 12 1 0 100 0 0 10 0
   2: 0200007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 13 1 0 100 0 0 10 0
   3: 00000000:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 14 1 0 100 0 0 10 0
`
	path := filepath.Join(t.TempDir(), "tcp")
	if err := os.WriteFile(path, []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}
	for port, want := range map[int]int{10810: 1000, 53: 0} {
		if uid, ok := listenerUIDIn(path, port); !ok || uid != want {
			t.Errorf("port %d: %d %v, want %d", port, uid, ok, want)
		}
	}
	// 127.0.0.2 is not the loopback address the redirect goes to; nothing
	// listens on 9999.
	for _, port := range []int{8080, 9999} {
		if _, ok := listenerUIDIn(path, port); ok {
			t.Errorf("port %d: found a listener", port)
		}
	}
}
