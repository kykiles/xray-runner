package subscription

import (
	"fmt"
	"testing"
	"time"
)

func TestBenchmarkResultString(t *testing.T) {
	cases := []struct {
		name   string
		result BenchmarkResult
		want   string
	}{
		{"timeout", BenchmarkResult{Error: fmt.Errorf("timeout")}, "timeout"},
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
