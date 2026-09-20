package app

// A run killed before its teardown — SIGKILL, a power cut, a console window
// closed faster than Windows lets a process clean up — leaves the system proxy
// pointing at its dead port on this machine. The next run cannot tell that value
// from a real setting: it snapshots it like any other and puts it back after the
// session, so one killed run takes the machine's internet away for good.
//
// Our own address on a dead port can only be that leftover, and nobody wants it
// back: it is cleared before the session snapshots it. Any other local proxy may
// be somebody's real setting whose original we have no record of (A11), so that
// one is only reported, loudly, and left alone.

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"xray-runner/internal/system"
)

// deadProxyHelp is where the manual fix is written up.
const deadProxyHelp = "README, «Частые проблемы» → «Сайты не открываются после аварийного выхода»"

// warnDeadLoopbackProxy handles a system proxy left pointing at a dead port on
// this machine. Called before the core starts, while nothing of ours listens
// yet, so the port being dead is a fact about the setting and not about timing.
// ports is this session's, and its HTTP port is the address Enable writes.
func (a *App) warnDeadLoopbackProxy(ports sessionPorts) {
	addr := deadLoopbackProxy(a.readProxyState(), a.proxyListening)
	if addr == "" {
		return
	}
	if addr == ourProxyAddr(ports.http) {
		a.clearDeadProxy(addr)
		return
	}
	slog.Warn("системный прокси указывает на локальный порт, где никто не слушает", "server", addr, "help", deadProxyHelp)
	a.noteOnStatus(fmt.Sprintf("Системный прокси указывает на %s, но там никто не слушает — похоже, прошлый запуск завершился аварийно. "+
		"Это не наш адрес, поэтому программа настройку не меняет и после сессии вернёт её как была. Как убрать вручную: %s.", addr, deadProxyHelp))
}

// clearDeadProxy takes our own leftover setting off the machine. It runs before
// the session snapshots the settings, so the snapshot — and with it what the
// teardown restores — is the machine without a proxy.
func (a *App) clearDeadProxy(addr string) {
	if err := a.clearProxy(); err != nil {
		slog.Error("не удалось снять оставшийся системный прокси", "server", addr, "error", err)
		a.noteOnStatus(fmt.Sprintf("Системный прокси указывает на наш адрес %s, где никто не слушает — он остался от запуска, "+
			"завершившегося аварийно, и снять его не удалось: %v. Как убрать вручную: %s.", addr, err, deadProxyHelp))
		return
	}
	slog.Warn("снят системный прокси, оставшийся от аварийно завершённого запуска", "server", addr)
	a.noteOnStatus(fmt.Sprintf("Системный прокси был включён на нашем адресе %s, где никто не слушает: он остался от запуска, "+
		"завершившегося аварийно, и снят — без этого интернет не работал бы и после выхода из программы.", addr))
}

// ourProxyAddr is the address Enable puts into the system settings for this
// session's HTTP port.
func ourProxyAddr(httpPort int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort))
}

// noteOnStatus queues a line for the status screen. Nothing is drawn yet when
// these run, so the note waits in pendingNote instead of being published.
func (a *App) noteOnStatus(note string) {
	if a.pendingNote != "" {
		note = a.pendingNote + " " + note
	}
	a.pendingNote = note
}

// deadLoopbackProxy returns the address of a manual system proxy on this
// machine that nothing listens on, or "" when there is none. A proxy that
// answers, one on another host, PAC and a switched-off proxy are all somebody's
// working setting.
func deadLoopbackProxy(s system.ProxyState, listening func(addr string) bool) string {
	if !s.Enabled {
		return ""
	}
	for _, addr := range proxyAddrs(s.Server) {
		host, _, _ := net.SplitHostPort(addr)
		if isLoopbackHost(host) && !listening(addr) {
			return addr
		}
	}
	return ""
}

// proxyAddrs pulls host:port pairs out of a proxy server setting: "host:port"
// as the runner writes it, with or without a scheme, or the Windows
// per-protocol list "http=host:port;https=host:port".
func proxyAddrs(server string) []string {
	var out []string
	for part := range strings.SplitSeq(server, ";") {
		part = strings.TrimSpace(part)
		if _, v, ok := strings.Cut(part, "="); ok {
			part = v
		}
		if _, v, ok := strings.Cut(part, "://"); ok {
			part = v
		}
		host, port, err := net.SplitHostPort(strings.TrimSuffix(part, "/"))
		if err != nil || host == "" || port == "" {
			continue
		}
		out = append(out, net.JoinHostPort(host, port))
	}
	return out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listeningAt reports whether anything accepts connections at addr right now.
func listeningAt(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
