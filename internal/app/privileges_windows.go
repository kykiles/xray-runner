//go:build windows

package app

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// checkTunPrivileges fails fast when tun mode is requested without elevation,
// so the user sees one clear error instead of 30s of xray retries (R-5).
func checkTunPrivileges() error {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID, windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid); err != nil {
		return nil // cannot determine, let xray try
	}
	defer windows.FreeSid(sid)

	elevated, err := windows.Token(0).IsMember(sid)
	if err != nil || elevated {
		return nil
	}
	return fmt.Errorf("режим TUN требует прав администратора — запустите от имени администратора")
}
