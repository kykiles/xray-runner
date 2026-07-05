//go:build linux

package system

import (
	"fmt"
	"os/exec"
	"strings"
)

func ReadProxyState() ProxyState {
	if state, ok := readGsettings(); ok {
		return state
	}
	if state, ok := readKDE(); ok {
		return state
	}
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
	mode, err := exec.Command("gsettings", "get", "org.gnome.system.proxy", "mode").Output()
	if err != nil {
		return ProxyState{}, false
	}
	enabled := strings.TrimSpace(string(mode)) == "'manual'"

	host, _ := exec.Command("gsettings", "get", "org.gnome.system.proxy.http", "host").Output()
	port, _ := exec.Command("gsettings", "get", "org.gnome.system.proxy.http", "port").Output()

	h := strings.Trim(string(host), "'\n\r ")
	p := strings.Trim(string(port), "'\n\r ")
	server := h
	if p != "" {
		server = h + ":" + p
	}

	overrides, _ := exec.Command("gsettings", "get", "org.gnome.system.proxy", "ignore-hosts").Output()
	overridesStr := trimGSettingsArray(overrides)

	return ProxyState{Enabled: enabled, Server: server, Overrides: overridesStr}, true
}

func writeGsettings(s ProxyState) bool {
	if s.Enabled {
		if exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "manual").Run() != nil {
			return false
		}
		host, port, _ := strings.Cut(s.Server, ":")
		if host == "" {
			host = "127.0.0.1"
			port = "10809"
		}
		if exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "host", host).Run() != nil {
			return false
		}
		if exec.Command("gsettings", "set", "org.gnome.system.proxy.http", "port", port).Run() != nil {
			return false
		}
		if exec.Command("gsettings", "set", "org.gnome.system.proxy.https", "host", host).Run() != nil {
			return false
		}
		if exec.Command("gsettings", "set", "org.gnome.system.proxy.https", "port", port).Run() != nil {
			return false
		}
		if err := setGSettingsIgnoreHosts(s.Overrides); err != nil {
			return false
		}
	} else {
		if exec.Command("gsettings", "set", "org.gnome.system.proxy", "mode", "none").Run() != nil {
			return false
		}
	}
	return true
}

func readKDE() (ProxyState, bool) {
	proxyType, err := exec.Command("kreadconfig5", "--group", "Proxy", "--key", "ProxyType").Output()
	if err != nil {
		return ProxyState{}, false
	}
	enabled := strings.TrimSpace(string(proxyType)) == "1"

	httpProxy, _ := exec.Command("kreadconfig5", "--group", "Proxy", "--key", "httpProxy").Output()
	server := strings.TrimSpace(string(httpProxy))

	noProxy, _ := exec.Command("kreadconfig5", "--group", "Proxy", "--key", "NoProxyFor").Output()
	overrides := strings.TrimSpace(string(noProxy))

	return ProxyState{Enabled: enabled, Server: server, Overrides: overrides}, true
}

func writeKDE(s ProxyState) bool {
	if s.Enabled {
		if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", "1").Run() != nil {
			return false
		}
		host, port, _ := strings.Cut(s.Server, ":")
		if host == "" {
			host = "127.0.0.1"
			port = "10809"
		}
		proxyVal := host + ":" + port
		if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "httpProxy", proxyVal).Run() != nil {
			return false
		}
		if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "httpsProxy", proxyVal).Run() != nil {
			return false
		}
		if s.Overrides != "" {
			if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "NoProxyFor", s.Overrides).Run() != nil {
				return false
			}
		}
	} else {
		if exec.Command("kwriteconfig5", "--group", "Proxy", "--key", "ProxyType", "0").Run() != nil {
			return false
		}
	}
	return true
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
			gv = append(gv, "'"+p+"'")
		}
	}
	gvariantStr := "[" + strings.Join(gv, ", ") + "]"
	return exec.Command("gsettings", "set", "org.gnome.system.proxy", "ignore-hosts", gvariantStr).Run()
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
