//go:build windows

package system

import (
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/sys/windows/registry"
)

const regKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// Seam for tests: the real implementations talk to HKCU, tests swap in a map.
var (
	regGetInt    = realRegGetInt
	regGetString = realRegGetString
	regSetInt    = realRegSetInt
	regSetString = realRegSetString
	regDelete    = realRegDelete
)

// ReadProxyState takes an exact snapshot of the three values we touch. A value
// that is absent and a value we failed to read are different things: the first
// must be deleted on restore, the second forbids us from touching anything at
// all. Only a fully read snapshot sets Read.
func ReadProxyState() ProxyState {
	var s ProxyState

	v, err := regGetInt("ProxyEnable")
	switch {
	case err == nil:
		s.Enabled = v != 0
		s.EnabledSet = true
	case errors.Is(err, registry.ErrNotExist):
		// No value: the proxy is off and there is nothing to put back.
	default:
		slog.Warn("не удалось прочитать ProxyEnable из реестра", "key", regKey, "error", err)
		return s
	}

	if s.Server, s.ServerSet, err = readProxyString("ProxyServer"); err != nil {
		slog.Warn("не удалось прочитать ProxyServer из реестра", "key", regKey, "error", err)
		return s
	}
	if s.Overrides, s.OverridesSet, err = readProxyString("ProxyOverride"); err != nil {
		slog.Warn("не удалось прочитать ProxyOverride из реестра", "key", regKey, "error", err)
		return s
	}

	s.Read = true
	return s
}

// WriteProxyState puts the machine back exactly as ReadProxyState found it.
func WriteProxyState(s ProxyState) error {
	if !s.Read {
		return errors.New("восстановление системного прокси: исходное состояние реестра не прочитано")
	}

	enabled := uint32(0)
	if s.Enabled {
		enabled = 1
	}
	if err := restoreInt("ProxyEnable", s.EnabledSet, enabled); err != nil {
		return err
	}
	if err := restoreString("ProxyServer", s.ServerSet, s.Server); err != nil {
		return err
	}
	if err := restoreString("ProxyOverride", s.OverridesSet, s.Overrides); err != nil {
		return err
	}

	notifyWinINet()
	return nil
}

func readProxyString(name string) (string, bool, error) {
	v, err := regGetString(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func restoreInt(name string, existed bool, v uint32) error {
	if !existed {
		return deleteProxyValue(name)
	}
	if err := regSetInt(name, v); err != nil {
		return fmt.Errorf("восстановление %s: %w", name, err)
	}
	return nil
}

func restoreString(name string, existed bool, v string) error {
	if !existed {
		return deleteProxyValue(name)
	}
	if err := regSetString(name, v); err != nil {
		return fmt.Errorf("восстановление %s: %w", name, err)
	}
	return nil
}

func deleteProxyValue(name string) error {
	err := regDelete(name)
	if err == nil || errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("удаление %s: %w", name, err)
}

func openProxyKey(access uint32) (registry.Key, error) {
	return registry.OpenKey(registry.CURRENT_USER, regKey, access)
}

func realRegGetInt(name string) (uint64, error) {
	k, err := openProxyKey(registry.QUERY_VALUE)
	if err != nil {
		return 0, err
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err
}

func realRegGetString(name string) (string, error) {
	k, err := openProxyKey(registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err
}

func realRegSetInt(name string, v uint32) error {
	k, err := openProxyKey(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(name, v)
}

func realRegSetString(name, v string) error {
	k, err := openProxyKey(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, v)
}

func realRegDelete(name string) error {
	k, err := openProxyKey(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(name)
}
