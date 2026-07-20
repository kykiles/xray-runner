package app

// A benchmark instance listens on both ports at once, so handing out the same
// number twice produces a config xray refuses to start — the server then shows
// as unreachable for a reason that has nothing to do with the server.

import "testing"

func TestFreePortPairReturnsDistinctPorts(t *testing.T) {
	for i := 0; i < 200; i++ {
		p, err := freePortPair()
		if err != nil {
			t.Fatalf("freePortPair: %v", err)
		}
		if p.socks == p.http {
			t.Fatalf("iteration %d: both inbounds got port %d", i, p.socks)
		}
	}
}
