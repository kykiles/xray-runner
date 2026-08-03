package subscription

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestBenchmarkResultString(t *testing.T) {
	cases := []struct {
		name   string
		result BenchmarkResult
		want   string
	}{
		{"generic_error", BenchmarkResult{Error: fmt.Errorf("boom")}, "ошибка"},
		{"timeout", BenchmarkResult{Error: fmt.Errorf("probe: %w", context.DeadlineExceeded)}, "timeout"},
		{"dns", BenchmarkResult{Error: fmt.Errorf("dial: %w", &net.DNSError{Err: "no such host"})}, "dns"},
		{"core_not_ready", BenchmarkResult{Error: fmt.Errorf("%w: порт", ErrCoreNotReady)}, "старт"},
		{"latency", BenchmarkResult{Latency: 45 * time.Millisecond}, "45ms"},
		{"latency_round", BenchmarkResult{Latency: 123456 * time.Microsecond}, "123ms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.result.String()
			if got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}
