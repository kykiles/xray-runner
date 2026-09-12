package app

import "testing"

// The TUN label used to promise "весь трафик через VPN" unconditionally. The
// panel profile routinely routes .ru domains and IP checkers direct, so the
// user saw their provider's address on an IP checker and read it as a broken
// VPN. The label now says which of the two it is (A03).
func TestStatusInfo_TunLabelAdmitsBypasses(t *testing.T) {
	a := newTemplateApp(t)
	a.mode = "tun"
	ports := sessionPorts{socks: 10808, http: 10809}

	if got := a.statusInfo(splitTarget(), ports).Mode; got != "TUN (весь трафик через VPN)" {
		t.Errorf("without bypasses Mode = %q", got)
	}

	a.hasBypass = true
	if got := a.statusInfo(splitTarget(), ports).Mode; got != "TUN (есть исключения по профилю)" {
		t.Errorf("with bypasses Mode = %q", got)
	}
}
