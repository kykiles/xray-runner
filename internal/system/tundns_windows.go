//go:build windows && (amd64 || arm64)

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"xray-runner/internal/xraycfg"

	"golang.org/x/sys/windows"
)

// TunSwitchesDNS enables the shared TUN DNS and server pinning path on Windows.
const TunSwitchesDNS = true

const wfpTunDNSDisplayName = "xray-runner DNS"

var (
	tunDNSMu     sync.Mutex
	tunDNSEngine windows.Handle
)

// EnableTunDNS sets the synthetic resolver on Wintun alone. A low metric
// gives it priority; the dynamic WFP filters prevent Windows from sending
// classical DNS through another adapter if it falls back. The filters
// survive Xray restarts and disappear if the process that holds them dies.
func EnableTunDNS(iface, core string) error {
	tunDNSMu.Lock()
	defer tunDNSMu.Unlock()

	link, err := net.InterfaceByName(iface)
	if err != nil {
		return errors.Join(err, closeTunDNSFilter())
	}
	if tunDNSEngine == 0 {
		if err := openTunDNSFilter(core); err != nil {
			return err
		}
	}

	out, err := powershell(fmt.Sprintf(
		"Set-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 -AutomaticMetric Disabled -InterfaceMetric 1 -PolicyStore ActiveStore -ErrorAction Stop; "+
			"Set-DnsClientServerAddress -InterfaceIndex %d -ServerAddresses '%s' -ErrorAction Stop",
		link.Index, link.Index, xraycfg.WindowsTunDNSAddr))
	if err != nil {
		return errors.Join(fmt.Errorf("настроить DNS адаптера %s: %w\n%s", iface, err, out),
			resetWinTunDNS(link.Index), closeTunDNSFilter())
	}
	slog.Info("tun dns enabled", "iface", iface, "server", xraycfg.WindowsTunDNSAddr)
	return nil
}

// DisableTunDNS releases the filter even when the core has already removed
// the adapter and with it the interface-specific settings.
func DisableTunDNS(iface string) error {
	tunDNSMu.Lock()
	defer tunDNSMu.Unlock()

	var resetErr error
	if link, err := net.InterfaceByName(iface); err == nil && tunDNSEngine != 0 {
		resetErr = resetWinTunDNS(link.Index)
	}
	return errors.Join(resetErr, closeTunDNSFilter())
}

func resetWinTunDNS(index int) error {
	out, err := powershell(fmt.Sprintf(
		"try { Set-DnsClientServerAddress -InterfaceIndex %d -ResetServerAddresses -ErrorAction Stop } "+
			"finally { Set-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 -AutomaticMetric Enabled -PolicyStore ActiveStore -ErrorAction Stop }",
		index, index))
	if err != nil {
		return fmt.Errorf("восстановить DNS адаптера TUN: %w\n%s", err, out)
	}
	return nil
}

func openTunDNSFilter(core string) error {
	app, err := wfpAppID(core)
	if err != nil {
		return err
	}
	defer wfpFree(app)
	engine, err := wfpOpenDynamic(wfpTunDNSDisplayName)
	if err != nil {
		return err
	}
	if err := wfpInstall(engine, wfpTunDNSDisplayName, tunDNSRules(), app, false); err != nil {
		_ = wfpClose(engine)
		return err
	}
	tunDNSEngine = engine
	return nil
}

func closeTunDNSFilter() error {
	if tunDNSEngine == 0 {
		return nil
	}
	if err := wfpClose(tunDNSEngine); err != nil {
		return err
	}
	tunDNSEngine = 0
	return nil
}
