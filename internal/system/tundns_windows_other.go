//go:build windows && !(amd64 || arm64)

package system

const TunSwitchesDNS = false

func EnableTunDNS(string, string) error { return nil }
func DisableTunDNS(string) error        { return nil }
