//go:build windows

package system

import "errors"

// ErrSplitUnsupported is returned where per-process routing has no mechanism.
// On Windows redirecting a chosen process needs a WFP callout driver — the
// userspace API can only permit or block by executable, never redirect — so the
// mode is absent rather than half-working.
var ErrSplitUnsupported = errors.New("маршрутизация по процессам поддерживается только на Linux")

func ListProcesses() ([]Process, error) { return nil, ErrSplitUnsupported }

func EnableSplit(names []string, tcpPort, dnsPort int) ([]string, error) {
	return nil, ErrSplitUnsupported
}

func DisableSplit() error { return nil }
