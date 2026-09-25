//go:build linux

package service

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestListenerUIDs(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2A3A 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11 1 0 100 0 0 10 0
   1: 0100007F:2A3A 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1001        0 12 1 0 100 0 0 10 0
   2: 0200007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 13 1 0 100 0 0 10 0
   3: 00000000:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 14 1 0 100 0 0 10 0
`
	// Two sockets on the DNS port — one of them another user's, bound with
	// SO_REUSEADDR — and a connected one, which receives nothing redirected.
	udp := `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  100: 0100007F:2A65 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 21 2 0 0
  101: 00000000:2A65 00000000:0000 07 00000000:00000000 00:00000000 00000000  1001        0 22 2 0 0
  102: 0100007F:2A65 0100007F:0035 01 00000000:00000000 00:00000000 00000000  1002        0 23 2 0 0
`
	dir := t.TempDir()
	tcpPath, udpPath := filepath.Join(dir, "tcp"), filepath.Join(dir, "udp")
	if err := os.WriteFile(tcpPath, []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(udpPath, []byte(udp), 0o644); err != nil {
		t.Fatal(err)
	}
	for port, want := range map[int][]int{10810: {1000}, 53: {0}} {
		if uids := listenerUIDsIn(tcpPath, "0A", port); !slices.Equal(uids, want) {
			t.Errorf("tcp port %d: %v, want %v", port, uids, want)
		}
	}
	// 127.0.0.2 is not the loopback address the redirect goes to; nothing
	// listens on 9999.
	for _, port := range []int{8080, 9999} {
		if uids := listenerUIDsIn(tcpPath, "0A", port); len(uids) != 0 {
			t.Errorf("tcp port %d: listeners %v", port, uids)
		}
	}
	if uids := listenerUIDsIn(udpPath, "07", 10853); !slices.Equal(uids, []int{1000, 1001}) {
		t.Errorf("udp port 10853: %v", uids)
	}
}
