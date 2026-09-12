//go:build windows

package system

import (
	"log/slog"

	"golang.org/x/sys/windows"
)

const (
	internetOptionRefresh         = 37
	internetOptionSettingsChanged = 39
)

var (
	wininet                = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOptionW = wininet.NewProc("InternetSetOptionW")
)

// notifyWinINet tells already running applications that the proxy settings
// changed. Without it they keep the old settings until they are restarted:
// the registry is correct, but nobody re-reads it.
//
// Best effort: the registry is already right by the time we get here, and new
// processes will read it correctly, so a failure must not turn a correct
// restore into an error.
func notifyWinINet() {
	if err := procInternetSetOptionW.Find(); err != nil {
		slog.Debug("wininet: InternetSetOptionW недоступна", "error", err)
		return
	}
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		if r, _, err := procInternetSetOptionW.Call(0, opt, 0, 0); r == 0 {
			slog.Debug("wininet: уведомление не прошло", "option", opt, "error", err)
		}
	}
}
