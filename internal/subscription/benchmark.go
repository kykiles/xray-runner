package subscription

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var ErrProtocolNotSupported = errors.New("protocol not supported for TCP ping")

// dialTimeout is overridable in tests to exercise timeout handling
// deterministically without touching the real network.
var dialTimeout = net.DialTimeout

type BenchmarkResult struct {
	Index   int
	Latency time.Duration
	Error   error
}

func RunBenchmark(entries []SubEntry, timeout time.Duration) []BenchmarkResult {
	results := make([]BenchmarkResult, len(entries))
	var wg sync.WaitGroup

	for i := range entries {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// A-5: hysteria2 is UDP-only, a TCP ping says nothing about it —
			// report "n/a" instead of a misleading timeout.
			if entries[idx].Protocol == "hysteria2" {
				results[idx] = BenchmarkResult{Index: idx, Error: ErrProtocolNotSupported}
				return
			}

			addr := net.JoinHostPort(entries[idx].Address, fmt.Sprintf("%d", entries[idx].Port))

			start := time.Now()
			conn, err := dialTimeout("tcp", addr, timeout)
			if err != nil {
				results[idx] = BenchmarkResult{Index: idx, Error: err}
				return
			}
			_ = conn.Close()
			results[idx] = BenchmarkResult{Index: idx, Latency: time.Since(start)}
		}(i)
	}

	wg.Wait()
	return results
}

func (r BenchmarkResult) String() string {
	if r.Error != nil {
		if errors.Is(r.Error, ErrProtocolNotSupported) {
			return "n/a"
		}
		return "timeout"
	}
	return fmt.Sprintf("%dms", r.Latency.Milliseconds())
}
