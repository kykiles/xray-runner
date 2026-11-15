//go:build windows

package system

// TunSwitchesDNS is false on Windows: a tun session there leaves the system's
// DNS alone, and the core keeps resolving its servers the usual way.
const TunSwitchesDNS = false

// EnableTunDNS does nothing on Windows.
func EnableTunDNS(string) error { return nil }

// DisableTunDNS does nothing on Windows.
func DisableTunDNS(string) error { return nil }
