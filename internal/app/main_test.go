package app

import (
	"os"
	"testing"
)

// TestMain keeps every test off the machine's resolver: a tun setup under test
// would otherwise reach systemd-resolved over the system bus — and the real
// xray-tun of a session running on the same machine — and a TUN config build
// would look the servers up on the network. Tests that look at either put
// their own fakes in.
func TestMain(m *testing.M) {
	enableTunDNS = func(string, string) error { return nil }
	disableTunDNS = func(string) error { return nil }
	lookupServer = func(string) []string { return nil }
	os.Exit(m.Run())
}
