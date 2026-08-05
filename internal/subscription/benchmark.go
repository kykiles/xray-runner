package subscription

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// ErrCoreNotReady means the measuring xray instance never opened its port, so
// nothing was measured at all. Distinct from a timeout, which is a verdict
// about the server rather than about our own core.
var ErrCoreNotReady = errors.New("xray не поднялся для замера")

// ErrConfigRejected means the core refused the config outright — a panel config
// written for a newer Xray than the one installed, most often. Split out of
// ErrCoreNotReady because the two need opposite things from the user: waiting
// longer never fixes this one, updating the core does.
var ErrConfigRejected = errors.New("ядро не приняло конфиг")

// BenchmarkResult is one measured item — a server or a whole profile. The
// measuring itself lives in internal/app: it runs the entry through a real xray
// instance, which is the only number that says anything about a proxy.
type BenchmarkResult struct {
	Index   int
	Latency time.Duration
	Error   error
}

// String is the label shown in the ping column. Every failure used to read
// "timeout", which made a dead server, an unresolvable domain and a core that
// never started indistinguishable — and the problem unteachable from a
// screenshot. The full error goes to the log; this is the one-word version.
//
// The labels are English throughout: the column is seven characters of
// diagnostics under an English header, and half-translating it only made "dns"
// and "ошибка" sit in the same column.
func (r BenchmarkResult) String() string {
	if r.Error == nil {
		return fmt.Sprintf("%dms", r.Latency.Milliseconds())
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(r.Error, &dnsErr):
		return "dns"
	case errors.Is(r.Error, ErrConfigRejected):
		return "config"
	case errors.Is(r.Error, ErrCoreNotReady):
		return "start"
	case errors.Is(r.Error, context.DeadlineExceeded) || os.IsTimeout(r.Error):
		return "timeout"
	case errors.Is(r.Error, context.Canceled):
		return "cancelled"
	}
	return "error"
}
