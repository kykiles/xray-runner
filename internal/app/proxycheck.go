package app

// A run killed before its teardown — SIGKILL, a power cut — leaves the system
// proxy pointing at its dead port on this machine. The next run cannot tell that
// value from a real setting: it snapshots it like any other and puts it back
// after the session. Without a record of what the killed run replaced, the exact
// original is gone (A11), so this only says so, loudly, and leaves the setting
// alone.

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"xray-runner/internal/system"
)

// deadProxyHelp is where the manual fix is written up.
const deadProxyHelp = "README, «Частые проблемы» → «Сайты не открываются после аварийного выхода»"

// warnDeadLoopbackProxy notes a system proxy left pointing at a dead port on
// this machine. Called before the core starts, while nothing of ours listens
// yet; it only reads the setting, and the session snapshots it as usual.
func (a *App) warnDeadLoopbackProxy() {
	addr := deadLoopbackProxy(a.readProxyState(), a.proxyListening)
	if addr == "" {
		return
	}
	slog.Warn("системный прокси указывает на локальный порт, где никто не слушает", "server", addr, "help", deadProxyHelp)
	note := fmt.Sprintf("Системный прокси указывает на %s, но там никто не слушает — похоже, прошлый запуск завершился аварийно. "+
		"Программа эту настройку не меняет и после сессии вернёт её как была. Как убрать вручную: %s.", addr, deadProxyHelp)
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
