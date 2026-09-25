//go:build windows && !(amd64 || arm64)

package system

import "errors"

// EnableKillSwitch refuses on 32-bit Windows: the WFP structures in
// firewall_windows.go are laid out for 64-bit only, and no 32-bit build ships.
func EnableKillSwitch(KillSwitchConfig) error {
	return errors.New("kill switch на 32-битной Windows не поддерживается")
}

// DisableKillSwitch has nothing to take down.
func DisableKillSwitch() error { return nil }
