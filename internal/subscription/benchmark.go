package subscription

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

var ErrProtocolNotSupported = errors.New("protocol not supported for TCP ping")

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

			addr := net.JoinHostPort(entries[idx].Address, fmt.Sprintf("%d", entries[idx].Port))

			start := time.Now()
			conn, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				results[idx] = BenchmarkResult{Index: idx, Error: err}
				return
			}
			conn.Close()
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
