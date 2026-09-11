//go:build windows

package system

import "errors"

// EnableKillSwitch refuses. The netsh rules it used to add blocked xray along
// with everything else — an explicit block beats an allow in Windows Firewall —
// and a working one needs WFP. config.Load already refuses KILL_SWITCH=true on
// Windows, so only a caller that skipped it gets here.
func EnableKillSwitch(KillSwitchConfig) error {
	return errors.New("kill switch на Windows не поддерживается")
}

// DisableKillSwitch has nothing to take down.
func DisableKillSwitch() error { return nil }
