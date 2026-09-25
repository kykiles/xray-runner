//go:build linux

package app

import (
	"errors"
	"os"

	"xray-runner/internal/netcap"
)

// checkTunPrivileges fails fast when tun mode is requested without the right
// to change the network, so the user sees one clear error instead of 30s of
// xray retries (R-5). Root has it, and so has a binary given CAP_NET_ADMIN with
// setcap, which hands it on to the core and to ip (H07).
func checkTunPrivileges() error {
	if os.Geteuid() == 0 || netcap.Delegated() {
		return nil
	}
	return errNoTunPrivileges
}

var errNoTunPrivileges = errors.New("режиму TUN нужно право менять сеть: установите службу один раз — " +
	"sudo xray-runner service install, — или выдайте право программе (sudo setcap cap_net_admin+ep <путь к xray-runner>), или запустите её через sudo")
