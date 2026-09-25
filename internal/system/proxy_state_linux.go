//go:build linux

package system

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// desktopCmd runs one desktop-configuration command and returns its stdout.
// Overridable in tests: gsettings and kwriteconfig5 write to the live session,
// so the restore logic can't be exercised against the real binaries.
var desktopCmd = func(bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	asDesktopUser(cmd)
	return cmd.Output()
}

// asDesktopUser sends the command into the desktop session of the user who ran
// sudo. Split tunnelling wants root, and gsettings as root talks to root's own
// dconf over a session bus that is not there: `gsettings set` still exits 0, so
// the proxy setting is silently dropped and the browser keeps going direct
// while the status screen says PROXY. Nothing to do when not under sudo.
func asDesktopUser(cmd *exec.Cmd) {
	if os.Geteuid() != 0 {
		return
	}
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err != nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	run := "/run/user/" + strconv.Itoa(uid)
	env := append(os.Environ(),
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+run+"/bus",
		"XDG_RUNTIME_DIR="+run,
	)
	// dconf keeps its database under $HOME, which sudo left pointing at root's.
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.HomeDir != "" {
		env = append(env, "HOME="+u.HomeDir)
	}
	cmd.Env = env
}

// gsettingsModes are the values GNOME accepts for the proxy mode. A saved Mode
// is only replayed if it is one of these — a state captured from KDE carries a
// numeric ProxyType, which would be rejected (or worse, misread) here.
var gsettingsModes = map[string]bool{"none": true, "manual": true, "auto": true}

// currentDesktop is XDG_CURRENT_DESKTOP; a var so tests can pick a desktop.
var currentDesktop = func() string { return os.Getenv("XDG_CURRENT_DESKTOP") }

// kdeFirst reports a KDE session, where KDE's own setting is the one to use.
// gsettings is installed on most KDE systems too — GTK apps pull it in — so
// trying it first wrote a proxy that KDE apps, and Chromium on KDE, never read,
// while the read-back confirmed it (G09). The variable is a colon-separated
// list ("KDE", "ubuntu:GNOME"). sudo usually drops it; the order then stays
// GNOME first, as before.
func kdeFirst() bool {
	return slices.Contains(strings.Split(currentDesktop(), ":"), "KDE")
}

func ReadProxyState() ProxyState {
	readers := []func() (ProxyState, bool){readGsettings, readKDE}
	if kdeFirst() {
		slices.Reverse(readers)
	}
	for _, read := range readers {
		if state, ok := read(); ok {
			return state
		}
	}
	slog.Warn("состояние прокси не прочитано: ни gsettings, ни kreadconfig не ответили")
	return ProxyState{}
}

func WriteProxyState(s ProxyState) error {
	if writeDesktop(s) {
		return nil
	}
	return fmt.Errorf("no supported desktop environment found (tried GNOME gsettings and KDE kwriteconfig)")
}

// writeDesktop writes s to the first desktop backend that takes it, in the
// order kdeFirst gives.
func writeDesktop(s ProxyState) bool {
	writers := []func(ProxyState) bool{writeGsettings, writeKDE}
	if kdeFirst() {
		slices.Reverse(writers)
	}
	for _, write := range writers {
		if write(s) {
			return true
		}
	}
	return false
}

func readGsettings() (ProxyState, bool) {
	mode, err := desktopCmd("gsettings", "get", "org.gnome.system.proxy", "mode")
	if err != nil {
		return ProxyState{}, false
	}
	rawMode := strings.Trim(strings.TrimSpace(string(mode)), "'")
	state := ProxyState{Enabled: rawMode == "manual", Mode: rawMode}

	var serverErr error
	read := func(schema string) string {
		host, err1 := desktopCmd("gsettings", "get", schema, "host")
		port, err2 := desktopCmd("gsettings", "get", schema, "port")
		if err1 != nil || err2 != nil {
			serverErr = fmt.Errorf("%s: %w", schema, errOr(err1, err2))
		}
		h := strings.Trim(string(host), "'\n\r ")
		p := strings.Trim(string(port), "'\n\r ")
		if p != "" {
			return h + ":" + p
		}
		return h
	}
	state.Server = read("org.gnome.system.proxy.http")
	state.SecureServer = read("org.gnome.system.proxy.https")
	state.ServerSet = serverErr == nil

	overrides, err := desktopCmd("gsettings", "get", "org.gnome.system.proxy", "ignore-hosts")
	state.Overrides = trimGSettingsArray(overrides)
	state.OverridesSet = err == nil
	return state, true
}

func errOr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
		return true
	}
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
	// The values the mode no longer points at go back too: Enable wrote ours
	// over them, and the user's own host, port and exceptions are theirs to
	// find again when they switch the mode back themselves (G09). Best-effort —
	// the mode, which is what decides where traffic goes, is already right.
	if s.ServerSet {
		if !writeGsettingsServer("org.gnome.system.proxy.http", s.Server) ||
			!writeGsettingsServer("org.gnome.system.proxy.https", s.SecureServer) {
			slog.Warn("прежние адрес и порт прокси GNOME не восстановлены")
		}
	}
	if s.OverridesSet {
		if err := setGSettingsIgnoreHosts(s.Overrides, true); err != nil {
			slog.Warn("прежние исключения прокси GNOME не восстановлены", "error", err)
		}
	}
	return true
}

// writeGsettingsServer writes one schema's host and port exactly as they were
// read: an empty host stays empty, rather than turning into the local proxy.
func writeGsettingsServer(schema, server string) bool {
	host, port, _ := strings.Cut(server, ":")
	if port == "" {
		port = "0"
	}
	if _, err := desktopCmd("gsettings", "set", schema, "host", host); err != nil {
		return false
	}
	_, err := desktopCmd("gsettings", "set", schema, "port", port)
	return err == nil
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
	secureHost, securePort := host, port
	if s.SecureServer != "" {
		if secureHost, securePort, _ = strings.Cut(s.SecureServer, ":"); securePort == "" {
			securePort = "0"
		}
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.http", "host", host); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.http", "port", port); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.https", "host", secureHost); err != nil {
		return false
	}
	if _, err := desktopCmd("gsettings", "set", "org.gnome.system.proxy.https", "port", securePort); err != nil {
		return false
	}
	return setGSettingsIgnoreHosts(s.Overrides, s.OverridesSet) == nil
}

// kdeVersion picks the KDE config tools: kreadconfig6/kwriteconfig6 on Plasma
// 6, the 5 ones before it. Both write the same kioslaverc.
func kdeVersion() string {
	if _, err := desktopCmd("kreadconfig6", "--group", "Proxy", "--key", "ProxyType"); err == nil {
		return "6"
	}
	return "5"
}

func readKDE() (ProxyState, bool) {
	read := "kreadconfig" + kdeVersion()
	proxyType, err := desktopCmd(read, "--group", "Proxy", "--key", "ProxyType")
	if err != nil {
		return ProxyState{}, false
	}
	rawType := strings.TrimSpace(string(proxyType))
	state := ProxyState{Enabled: rawType == "1", Mode: rawType}

	httpProxy, err1 := desktopCmd(read, "--group", "Proxy", "--key", "httpProxy")
	httpsProxy, err2 := desktopCmd(read, "--group", "Proxy", "--key", "httpsProxy")
	state.Server = strings.TrimSpace(string(httpProxy))
	state.SecureServer = strings.TrimSpace(string(httpsProxy))
	state.ServerSet = err1 == nil && err2 == nil

	noProxy, err := desktopCmd(read, "--group", "Proxy", "--key", "NoProxyFor")
	state.Overrides = strings.TrimSpace(string(noProxy))
	state.OverridesSet = err == nil
	return state, true
}

func writeKDE(s ProxyState) bool {
	write := "kwriteconfig" + kdeVersion()
	if s.Enabled {
		if _, err := desktopCmd(write, "--group", "Proxy", "--key", "ProxyType", "1"); err != nil {
			return false
		}
		if !writeKDEManual(write, s) {
			// R-2: same rollback as gsettings — don't leave ProxyType=1 with a
			// half-configured proxy.
			_, _ = desktopCmd(write, "--group", "Proxy", "--key", "ProxyType", "0")
			return false
		}
		return true
	}
	// Same reasoning as gsettings: ProxyType 2 (PAC) or 3 (system) must come
	// back as it was. Only a numeric mode is replayed — a GNOME state would
	// carry "auto" here, which KDE does not understand.
	proxyType := "0"
	if s.Mode != "" && s.Mode != "1" && isNumeric(s.Mode) {
		proxyType = s.Mode
	}
	if _, err := desktopCmd(write, "--group", "Proxy", "--key", "ProxyType", proxyType); err != nil {
		return false
	}
	// As for GNOME: the values Enable wrote over go back, best-effort.
	if s.ServerSet {
		for key, v := range map[string]string{"httpProxy": s.Server, "httpsProxy": s.SecureServer} {
			if _, err := desktopCmd(write, "--group", "Proxy", "--key", key, v); err != nil {
				slog.Warn("прежний адрес прокси KDE не восстановлен", "key", key, "error", err)
			}
		}
	}
	if s.OverridesSet {
		if _, err := desktopCmd(write, "--group", "Proxy", "--key", "NoProxyFor", s.Overrides); err != nil {
			slog.Warn("прежние исключения прокси KDE не восстановлены", "error", err)
		}
	}
	return true
}

// writeKDEManual applies the proxy values for the already-enabled ProxyType=1;
// false means the caller must roll ProxyType back (R-2).
func writeKDEManual(write string, s ProxyState) bool {
	host, port := splitProxyServer(s.Server)
	proxyVal := host
	// A trailing colon is not a valid proxy address, so a portless host stays
	// bare rather than becoming "host:".
	if port != "" {
		proxyVal = host + ":" + port
	}
	secureVal := proxyVal
	if s.SecureServer != "" {
		secureVal = s.SecureServer
	}
	if _, err := desktopCmd(write, "--group", "Proxy", "--key", "httpProxy", proxyVal); err != nil {
		return false
	}
	if _, err := desktopCmd(write, "--group", "Proxy", "--key", "httpsProxy", secureVal); err != nil {
		return false
	}
	if s.Overrides != "" || s.OverridesSet {
		if _, err := desktopCmd(write, "--group", "Proxy", "--key", "NoProxyFor", s.Overrides); err != nil {
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

// setGSettingsIgnoreHosts writes the exceptions list. An empty list is left
// alone unless it is known to be what the desktop had (set), in which case it
// is written as the empty array it was.
func setGSettingsIgnoreHosts(overrides string, set bool) error {
	if overrides == "" && !set {
		return nil
	}
	var gv []string
	for p := range strings.SplitSeq(overrides, ",") {
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

// trimGSettingsArray reads a gsettings string array into a comma list. An empty
// one prints with its type, "@as []", and is the empty list — not the entry
// "@as" (G09).
func trimGSettingsArray(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	s = strings.TrimPrefix(s, "@as ")
	s = strings.Trim(s, "[]")
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "'")
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}
