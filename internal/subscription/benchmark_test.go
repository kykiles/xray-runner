package subscription

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunBenchmarkReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, _ := ln.Accept()
			if conn != nil {
				conn.Close()
			}
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	entry := SubEntry{Address: addr.IP.String(), Port: addr.Port}

	results := RunBenchmark([]SubEntry{entry}, 2*time.Second)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != nil {
		t.Fatalf("unexpected error: %v", results[0].Error)
	}
	if results[0].Latency < 0 {
		t.Errorf("expected non-negative latency, got %v", results[0].Latency)
	}
	if results[0].Latency >= time.Second {
		t.Errorf("latency too high for localhost: %v", results[0].Latency)
	}
}

func TestRunBenchmarkUnreachable(t *testing.T) {
	entry := SubEntry{Address: "127.0.0.1", Port: 19999}

	results := RunBenchmark([]SubEntry{entry}, 200*time.Millisecond)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("expected error for unreachable port")
	}
}

func TestRunBenchmarkEmpty(t *testing.T) {
	results := RunBenchmark(nil, time.Second)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestRunBenchmarkConcurrent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, _ := ln.Accept()
			if conn != nil {
				conn.Close()
			}
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	entries := make([]SubEntry, 5)
	for i := range entries {
		entries[i] = SubEntry{Address: addr.IP.String(), Port: addr.Port}
	}

	results := RunBenchmark(entries, 2*time.Second)

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Error != nil {
			t.Errorf("entry %d: unexpected error: %v", i, r.Error)
		}
		if r.Index != i {
			t.Errorf("entry %d: Index = %d, want %d", i, r.Index, i)
		}
	}
}

func TestRunBenchmarkHysteria2Reachable(t *testing.T) {
	entry := SubEntry{Protocol: "hysteria2", Address: "127.0.0.1", Port: 12345}

	results := RunBenchmark([]SubEntry{entry}, 200*time.Millisecond)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("expected error for closed port")
	}
}

func TestRunBenchmarkHysteria2Unreachable(t *testing.T) {
	entry := SubEntry{Protocol: "hysteria2", Address: "127.0.0.1", Port: 19999}

	results := RunBenchmark([]SubEntry{entry}, 200*time.Millisecond)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("expected error for closed port")
	}
}

func TestRunBenchmarkMixedProtocols(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, _ := ln.Accept()
			if conn != nil {
				conn.Close()
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)

	entries := []SubEntry{
		{Protocol: "vless", Address: addr.IP.String(), Port: addr.Port},
		{Protocol: "hysteria2", Address: addr.IP.String(), Port: addr.Port},
	}

	results := RunBenchmark(entries, 2*time.Second)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Error != nil {
		t.Errorf("entry 0 (vless): unexpected error: %v", results[0].Error)
	}
	if results[1].Error != nil {
		t.Errorf("entry 1 (hysteria2): unexpected error: %v", results[1].Error)
	}
}

func TestBenchmarkResultString(t *testing.T) {
	cases := []struct {
		name   string
		result BenchmarkResult
		want   string
	}{
		{"not_supported", BenchmarkResult{Error: ErrProtocolNotSupported}, "n/a"},
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

func TestRunBenchmarkTimeoutPropagation(t *testing.T) {
	entry := SubEntry{Address: "10.255.255.1", Port: 443}

	start := time.Now()
	results := RunBenchmark([]SubEntry{entry}, 200*time.Millisecond)
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(results[0].Error.Error(), "timeout") && !strings.Contains(results[0].Error.Error(), "deadline") {
		t.Logf("expected timeout-like error, got: %v", results[0].Error)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("benchmark took too long: %v, want ~200ms", elapsed)
	}
}
