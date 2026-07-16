package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

type portPair struct{ socks, http int }

// freePort asks the OS for an unused TCP port on loopback by binding :0 and
// reading back the assigned port, then releasing it. This is racy (TOCTOU: the
// port can be taken between close and xray's bind), but far more reliable than
// handing out incrementing numbers that may already be in use (P-2).
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// freePortPair returns two distinct free loopback ports for the socks/http
// inbounds of a benchmark instance.
func freePortPair() (portPair, error) {
	socks, err := freePort()
	if err != nil {
		return portPair{}, err
	}
	http, err := freePort()
	if err != nil {
		return portPair{}, err
	}
	return portPair{socks: socks, http: http}, nil
}

type ProxyBenchmarker struct {
	template      *xraycfg.XrayConfig
	xrayBinary    string
	concurrency   int
	timeout       time.Duration
	allowInsecure bool
	tmpDir        string
}

func NewProxyBenchmarker(template *xraycfg.XrayConfig, binary string, concurrency int, timeout time.Duration, allowInsecure bool) *ProxyBenchmarker {
	return &ProxyBenchmarker{
		template:      template,
		xrayBinary:    binary,
		concurrency:   concurrency,
		timeout:       timeout,
		allowInsecure: allowInsecure,
	}
}

func waitPort(ctx context.Context, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		// P-1: abort the wait promptly when the benchmark is cancelled instead
		// of sleeping out the whole timeout.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// benchInbounds are the loopback inbounds a measured instance listens on. They
// replace whatever inbounds the config came with: only the outbound path is
// under test, and the real ports may be in use by the running session.
func benchInbounds(ports portPair) []xraycfg.Inbound {
	return []xraycfg.Inbound{
		{
			Tag: "socks", Port: ports.socks, Listen: "127.0.0.1",
			Protocol: "socks",
			Settings: json.RawMessage(`{"udp":true,"auth":"noauth"}`),
			Sniffing: &xraycfg.SniffingConfig{
				Enabled: true, RouteOnly: true,
				DestOverride: []string{"http", "tls", "quic"},
			},
		},
		{
			Tag: "http", Port: ports.http, Listen: "127.0.0.1",
			Protocol: "http",
			Settings: json.RawMessage(`{"allowTransparent":false}`),
			Sniffing: &xraycfg.SniffingConfig{
				Enabled: true, RouteOnly: true,
				DestOverride: []string{"http", "tls", "quic"},
			},
		},
	}
}

// buildProfileBenchConfig measures the profile the way it will actually run:
// its own outbounds, routing and balancer are kept untouched, so the number
// reflects the provider's balancing rules rather than one server picked by us.
func buildProfileBenchConfig(p subscription.Profile, ports portPair, logLevel string) (json.RawMessage, error) {
	return xraycfg.MergeProfile(p.Raw, benchInbounds(ports), logLevel)
}

func (pb *ProxyBenchmarker) measureOne(ctx context.Context, entry subscription.SubEntry, ports portPair) subscription.BenchmarkResult {
	if err := entry.Validate(); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	entry.AllowInsecure = pb.allowInsecure
	outboundJSON, err := subscription.ProxyOutboundJSON(&entry)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}

	inbounds := benchInbounds(ports)

	outbounds := []json.RawMessage{outboundJSON}
	for _, ob := range pb.template.Outbounds {
		outbounds = append(outbounds, ob)
	}

	cfg := &xraycfg.XrayConfig{
		Log:       &xraycfg.LogConfig{Loglevel: "error"},
		DNS:       pb.template.DNS,
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Routing:   xraycfg.AddCatchAllRule(pb.template.Routing),
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	return pb.runAndMeasure(ctx, data, ports)
}

// measureProfile times the profile as a whole, through its own balancer.
func (pb *ProxyBenchmarker) measureProfile(ctx context.Context, p subscription.Profile, ports portPair) subscription.BenchmarkResult {
	// A bare link carries no panel config: it is a single server wearing a
	// profile's clothes, so measure it as one.
	if len(p.Raw) == 0 {
		if len(p.Entries) == 0 {
			return subscription.BenchmarkResult{Error: fmt.Errorf("профиль без серверов")}
		}
		return pb.measureOne(ctx, p.Entries[0], ports)
	}

	data, err := buildProfileBenchConfig(p, ports, "error")
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	return pb.runAndMeasure(ctx, data, ports)
}

// runAndMeasure starts xray on the given config and times a single request
// through its http inbound.
func (pb *ProxyBenchmarker) runAndMeasure(ctx context.Context, cfgJSON []byte, ports portPair) subscription.BenchmarkResult {
	tmpFile := filepath.Join(pb.tmpDir, fmt.Sprintf("xray-bench-%d-%d.json", ports.socks, ports.http))
	// H-2: bench configs carry the same secrets as the main config; keep them
	// 0600 inside a private 0700 dir instead of world-readable /tmp.
	if err := os.WriteFile(tmpFile, cfgJSON, 0600); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	defer func() { _ = os.Remove(tmpFile) }()

	runner := xray.New(pb.xrayBinary, tmpFile)
	// P-1: propagate ctx so Ctrl+C tears down the xray instance mid-measure.
	if err := runner.Start(ctx); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	defer func() { _ = runner.Stop() }()

	if !waitPort(ctx, ports.http, pb.timeout) {
		return subscription.BenchmarkResult{Error: fmt.Errorf("port %d not ready within timeout", ports.http)}
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", ports.http)
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.google.com/generate_204", nil)
	resp, err := client.Do(req)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return subscription.BenchmarkResult{
			Error: fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}

	return subscription.BenchmarkResult{Latency: time.Since(start)}
}

func (pb *ProxyBenchmarker) Run(ctx context.Context, entries []subscription.SubEntry, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
	return runBatch(ctx, pb, entries, pb.measureOne, onResult)
}

// RunProfiles measures whole profiles instead of single servers: each one is
// timed through its own balancer, which is what the user actually connects to.
func (pb *ProxyBenchmarker) RunProfiles(ctx context.Context, profiles []subscription.Profile, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
	return runBatch(ctx, pb, profiles, pb.measureProfile, onResult)
}

// runBatch measures items concurrently into a private temp dir. Servers and
// profiles differ only in how one item is measured, so everything around that —
// the temp dir, the worker pool, the result plumbing — is shared.
func runBatch[T any](
	ctx context.Context,
	pb *ProxyBenchmarker,
	items []T,
	measure func(context.Context, T, portPair) subscription.BenchmarkResult,
	onResult func(subscription.BenchmarkResult),
) []subscription.BenchmarkResult {
	dir, err := os.MkdirTemp("", "xray-bench-*")
	if err != nil {
		results := make([]subscription.BenchmarkResult, len(items))
		for i := range results {
			results[i] = subscription.BenchmarkResult{Index: i, Error: err}
		}
		return results
	}
	pb.tmpDir = dir
	defer func() { _ = os.RemoveAll(dir) }()

	results := make([]subscription.BenchmarkResult, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, pb.concurrency)
	// Q-3: onResult runs from worker goroutines; serialize the callbacks here so
	// consumers (e.g. the progress line in menu.go) don't need their own locking.
	var resultMu sync.Mutex

	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			var result subscription.BenchmarkResult
			select {
			case <-ctx.Done():
				result = subscription.BenchmarkResult{Error: ctx.Err()}
			default:
				ports, err := freePortPair()
				if err != nil {
					result = subscription.BenchmarkResult{Error: err}
				} else {
					result = measure(ctx, items[idx], ports)
				}
			}
			result.Index = idx
			results[idx] = result
			if onResult != nil {
				resultMu.Lock()
				onResult(result)
				resultMu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	return results
}
