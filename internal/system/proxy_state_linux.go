//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// desktopCmd runs one desktop-configuration command and returns its stdout.
// Overridable in tests: gsettings and kwriteconfig5 write to the live session,
// so the restore logic can't be exercised against the real binaries.
var desktopCmd = func(bin string, args ...string) ([]byte, error) {
	return exec.Command(bin, args...).Output()
}

// gsettingsModes are the values GNOME accepts for the proxy mode. A saved Mode
// is only replayed if it is one of these — a state captured from KDE carries a
// numeric ProxyType, which would be rejected (or worse, misread) here.
var gsettingsModes = map[string]bool{"none": true, "manual": true, "auto": true}

func ReadProxyState() ProxyState {
	if state, ok := readGsettings(); ok {
		return state
	}
	if state, ok := readKDE(); ok {
		return state
	}
	slog.Warn("состояние прокси не прочитано: ни gsettings, ни kreadconfig5 не ответили")
	return ProxyState{}
}

func WriteProxyState(s ProxyState) error {
	if writeGsettings(s) {
		return nil
	}
	if writeKDE(s) {
		return nil
	}
	return fmt.Errorf("no supported desktop environment found (tried GNOME gsettings and KDE kwriteconfig5)")
}

func readGsettings() (ProxyState, bool) {
	mode, err := desktopCmd("gsettings", "get", "org.gnome.system.proxy", "mode")
	if err != nil {
		return ProxyState{}, false
	}
	rawMode := strings.Trim(strings.TrimSpace(string(mode)), "'")
	enabled := rawMode == "manual"

	host, _ := desktopCmd("gsettings", "get", "org.gnome.system.proxy.http", "host")
	port, _ := desktopCmd("gsettings", "get", "org.gnome.system.proxy.http", "port")

	h := strings.Trim(string(host), "'\n\r ")
	p := strings.Trim(string(port), "'\n\r ")
	server := h
	if p != "" {
		server = h + ":" + p
	}

	overrides, _ := desktopCmd("gsettings", "get", "org.gnome.system.proxy", "ignore-hosts")
	overridesStr := trimGSettingsArray(overrides)

	return ProxyState{Enabled: enabled, Mode: rawMode, Server: server, Overrides: overridesStr}, true
}

func writeGsettings(s ProxyState) bool {
	if s.Enabled {
		if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy", "mode", "manual"); err != nil {
			return false
		}
		if !writeGsettingsManual(s) {
			// R-2: mode is already "manual" — roll back to "none" so the desktop
			// isn't left pointing at a half-configured proxy.
			_, _ = desktopCmd("gsettings", "set", "org.gnome.system.proxy", "mode", "none")
			return false
		}
	} else {
		// Restore the mode the desktop actually had. Forcing "none" would turn a
		// PAC-configured desktop (auto) into no proxy at all, which the user
		// never asked for and gets no warning about.
		mode := "none"
		if s.Mode != "" && s.Mode != "manual" && gsettingsModes[s.Mode] {
			mode = s.Mode
		}
		if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy", "mode", mode); err != nil {
			return false
		}
	}
	return true
}

// writeGsettingsManual applies host/port/ignore-hosts for the already-enabled
// manual mode; false means the caller must roll the mode back (R-2).
func writeGsettingsManual(s ProxyState) bool {
	host, port := splitProxyServer(s.Server)
	// The port key is an int: handing it an empty string fails the write and
	// takes the whole restore down with it.
	if port == "" {
		port = "0"
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.http", "host", host); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.http", "port", port); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.https", "host", host); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.https", "port", port); err != nil {
		return false
	}
	return setGSettingsIgnoreHosts(s.Overrides) == nil
}

func readKDE() (ProxyState, bool) {
	proxyType, err := desktopCmd("kreadconfig5", "--group", "Proxy", "--key", "ProxyType")
	if err != nil {
		return ProxyState{}, false
	}
	rawType := strings.TrimSpace(string(proxyType))
	enabled := rawType == "1"

	httpProxy, _ := desktopCmd("kreadconfig5", "--group", "Proxy", "--key", "httpProxy")
	server := strings.TrimSpace(string(httpProxy))

	noProxy, _ := desktopCmd("kreadconfig5", "--group", "Proxy", "--key", "NoProxyFor")
	overrides := strings.TrimSpace(string(noProxy))

	return ProxyState{Enabled: enabled, Mode: rawType, Server: server, Overrides: overrides}, true
}

func writeKDE(s ProxyState) bool {
	if s.Enabled {
		if _, err := desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", "1"); err != nil {
			return false
		}
		if !writeKDEManual(s) {
			// R-2: same rollback as gsettings — don't leave ProxyType=1 with a
			// half-configured proxy.
			_, _ = desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", "0")
			return false
		}
	} else {
		// Same reasoning as gsettings: ProxyType 2 (PAC) or 3 (system) must come
		// back as it was. Only a numeric mode is replayed — a GNOME state would
		// carry "auto" here, which KDE does not understand.
		proxyType := "0"
		if s.Mode != "" && s.Mode != "1" && isNumeric(s.Mode) {
			proxyType = s.Mode
		}
		if _, err := desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", proxyType); err != nil {
			return false
		}
	}
	return true
}

// writeKDEManual applies the proxy values for the already-enabled ProxyType=1;
// false means the caller must roll ProxyType back (R-2).
func writeKDEManual(s ProxyState) bool {
	host, port := splitProxyServer(s.Server)
	proxyVal := host
	// A trailing colon is not a valid proxy address, so a portless host stays
	// bare rather than becoming "host:".
	if port != "" {
		proxyVal = host + ":" + port
	}
	if _, err := desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "httpProxy", proxyVal); err != nil {
		return false
	}
	if _, err := desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "httpsProxy", proxyVal); err != nil {
		return false
	}
	if s.Overrides != "" {
		if _, err := desktopCmd("kwriteconfig5", "--group", "Proxy", "--key", "NoProxyFor", s.Overrides); err != nil {
			return false
		}
	}
	return true
}

// splitProxyServer splits "host:port", falling back to the local proxy when
// there is no host to restore at all.
func splitProxyServer(server string) (host, port string) {
	host, port, _ = strings.Cut(server, ":")
	if host == "" {
		return "127.0.0.1", "10809"
	}
	return host, port
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func setGSettingsIgnoreHosts(overrides string) error {
	if overrides == "" {
		return nil
	}
	parts := strings.Split(overrides, ",")
	var gv []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			// S-4: escape single quotes so a value containing ' doesn't break the
			// GVariant array literal passed to gsettings.
			escaped := strings.ReplaceAll(p, "'", `\'`)
			gv = append(gv, "'"+escaped+"'")
		}
	}
	gvariantStr := "[" + strings.Join(gv, ", ") + "]"
	_, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy", "ignore-hosts", gvariantStr)
	return err
}

func trimGSettingsArray(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "'")
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}
