package tui

// Server-side types and helpers shared by the list screen: the benchmark and
// config callbacks, the endpoint-keyed ping cache, and the small helpers that
// turn a SubEntry into a table row. The list model itself lives in
// list_screen.go.

import (
	"context"
	"fmt"
	"strings"

	"xray-runner/internal/subscription"
)

// BenchmarkFunc mirrors the app benchmarker: onResult fires per finished entry.
type BenchmarkFunc func(ctx context.Context, entries []subscription.SubEntry, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult

// ServerConfigFunc renders the full xray config a session with this server
// would run — the panel's dns and routing included, not just the outbound.
// profIdx names the profile the server was picked from (-1 for a flat server),
// so the preview runs under the same routing the connection will.
type ServerConfigFunc func(e *subscription.SubEntry, profIdx int) (string, error)

// SaveConfigFunc writes a shown config to disk under the given name and returns
// the path it landed at.
type SaveConfigFunc func(name, text string) (string, error)

// PingCache holds the last measurement of every server pinged this run, so
// returning to the list after a connection still shows what was measured
// instead of an empty column — a ping run is slow, and the numbers are only
// there to say roughly where each server stands. It is keyed by endpoint, not
// by position: a refresh or another profile shifts the indices, the endpoint
// stays. `r` on the list screen drops it — that is the way to force a fresh
// measurement.
type PingCache map[string]subscription.BenchmarkResult

func pingKey(e subscription.SubEntry) string {
	return fmt.Sprintf("%s:%d", e.Address, e.Port)
}

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"trojan":    true,
	"hysteria2": true,
	"hysteria":  true,
}

type benchResultMsg struct {
	gen int
	subscription.BenchmarkResult
}
type benchDoneMsg struct{ gen int }

// indexOfServer finds the entry matching address:port, or 0 when there is no
// match — address:port identifies a server across a subscription refresh, where
// names may change and positions shift.
func indexOfServer(entries []subscription.SubEntry, address string, port int) int {
	if address == "" {
		return 0
	}
	for i, e := range entries {
		if e.Address == address && e.Port == port {
			return i
		}
	}
	return 0
}

// entryHaystack is the entry flattened to one lowercase string spanning every
// column, so the filter can match a substring anywhere: name, host, port,
// protocol or transport.
func entryHaystack(e subscription.SubEntry) string {
	return strings.ToLower(fmt.Sprintf("%s %s %d %s %s",
		e.Remarks, e.Address, e.Port, e.Protocol, e.Network))
}

// mark is the server's name from the subscription. Xray-config subscriptions
// carry no per-server name, so the parser falls back to the address — repeating
// it in the NAME column would just duplicate HOST.
func mark(e subscription.SubEntry) string {
	if e.Remarks == e.Address {
		return ""
	}
	return e.Remarks
}
