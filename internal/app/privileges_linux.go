//go:build linux

package app

import (
	"fmt"
	"os"
)

// checkTunPrivileges fails fast when tun mode is requested without root, so
// the user sees one clear error instead of 30s of xray retries (R-5).
func checkTunPrivileges() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("режим TUN требует прав root — запустите через sudo")
	}
	return nil
}
