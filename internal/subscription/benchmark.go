package subscription

import (
	"fmt"
	"time"
)

// BenchmarkResult is one measured item — a server or a whole profile. The
// measuring itself lives in internal/app: it runs the entry through a real xray
// instance, which is the only number that says anything about a proxy.
type BenchmarkResult struct {
	Index   int
	Latency time.Duration
	Error   error
}

func (r BenchmarkResult) String() string {
	if r.Error != nil {
		return "timeout"
	}
	return fmt.Sprintf("%dms", r.Latency.Milliseconds())
}
