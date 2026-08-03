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
func (r BenchmarkResult) String() string {
	if r.Error == nil {
		return fmt.Sprintf("%dms", r.Latency.Milliseconds())
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(r.Error, &dnsErr):
		return "dns"
	case errors.Is(r.Error, ErrCoreNotReady):
		return "старт"
	case errors.Is(r.Error, context.DeadlineExceeded) || os.IsTimeout(r.Error):
		return "timeout"
	case errors.Is(r.Error, context.Canceled):
		return "отменён"
	}
	return "ошибка"
}
