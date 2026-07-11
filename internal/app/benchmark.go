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

func addCatchAllRouting(templateRouting json.RawMessage) json.RawMessage {
	var routing map[string]interface{}
	if templateRouting != nil {
		json.Unmarshal(templateRouting, &routing)
	}
	if routing == nil {
		routing = map[string]interface{}{}
	}
	rules, _ := routing["rules"].([]interface{})
	rules = append(rules, map[string]interface{}{
		"type": "field", "outboundTag": "proxy", "network": "tcp,udp",
	})
	routing["rules"] = rules
	modifiedRouting, _ := json.Marshal(routing)
	return modifiedRouting
}

type portPair struct{ socks, http int }

type portAllocator struct {
	mu   sync.Mutex
	base int
	next int
}

func newPortAllocator(base int) *portAllocator {
	return &portAllocator{base: base, next: 0}
}

func (a *portAllocator) alloc() portPair {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.next
	a.next++
	return portPair{socks: a.base + n, http: a.base + 100 + n}
}

type ProxyBenchmarker struct {
	template    *xraycfg.XrayConfig
	xrayBinary  string
	concurrency int
	timeout     time.Duration
}

func NewProxyBenchmarker(template *xraycfg.XrayConfig, binary string, concurrency int, timeout time.Duration) *ProxyBenchmarker {
	return &ProxyBenchmarker{
		template:    template,
		xrayBinary:  binary,
		concurrency: concurrency,
		timeout:     timeout,
	}
}

func waitPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (pb *ProxyBenchmarker) measureOne(entry subscription.SubEntry, ports portPair) subscription.BenchmarkResult {
	outboundJSON, err := subscription.BuildOutboundJSON(&entry)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}

	inbounds := []xraycfg.Inbound{
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

	outbounds := []json.RawMessage{outboundJSON}
	for _, ob := range pb.template.Outbounds {
		outbounds = append(outbounds, ob)
	}

	cfg := &xraycfg.XrayConfig{
		Log:       &xraycfg.LogConfig{Loglevel: "error"},
		DNS:       pb.template.DNS,
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Routing:   addCatchAllRouting(pb.template.Routing),
	}

	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("xray-bench-%d-%d.json", ports.socks, ports.http))
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	defer os.Remove(tmpFile)

	runner := xray.New(pb.xrayBinary, tmpFile)
	if err := runner.Start(context.Background()); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	defer runner.Stop()

	if !waitPort(ports.http, pb.timeout) {
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
	req, _ := http.NewRequest("GET", "https://www.google.com/generate_204", nil)
	resp, err := client.Do(req)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	resp.Body.Close()

	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return subscription.BenchmarkResult{
			Error: fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}

	return subscription.BenchmarkResult{Latency: time.Since(start)}
}

func (pb *ProxyBenchmarker) Run(ctx context.Context, entries []subscription.SubEntry, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult {
	results := make([]subscription.BenchmarkResult, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, pb.concurrency)
	alloc := newPortAllocator(10810)

	for i := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			select {
			case <-ctx.Done():
				results[idx] = subscription.BenchmarkResult{Index: idx, Error: ctx.Err()}
			default:
				result := pb.measureOne(entries[idx], alloc.alloc())
				result.Index = idx
				results[idx] = result
				if onResult != nil {
					onResult(result)
				}
			}
		}(i)
	}

	wg.Wait()
	return results
}
