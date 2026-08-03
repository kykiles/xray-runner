package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

type portPair struct{ socks, http int }

// freePortPair asks the OS for two unused TCP ports on loopback by binding :0
// twice and reading back the assigned ports. Both listeners stay open until the
// pair is complete, otherwise the second bind may be handed the port the first
// one just released and the instance would try to listen twice on it. This is
// still racy (TOCTOU: a port can be taken between close and xray's bind), but
// far more reliable than handing out incrementing numbers (P-2).
func freePortPair() (portPair, error) {
	socks, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return portPair{}, err
	}
	defer func() { _ = socks.Close() }()

	http, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return portPair{}, err
	}
	defer func() { _ = http.Close() }()

	return portPair{
		socks: socks.Addr().(*net.TCPAddr).Port,
		http:  http.Addr().(*net.TCPAddr).Port,
	}, nil
}

type ProxyBenchmarker struct {
	template      *xraycfg.XrayConfig
	xrayBinary    string
	concurrency   int
	timeout       time.Duration
	allowInsecure bool
	checkURLs     []string
	probeHosts    []string
}

// probeHosts turns the check URLs into exact-match routing domains, so the
// probe rule matches those hosts and nothing else.
func probeHosts(urls []string) []string {
	var hosts []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
		}
		hosts = append(hosts, "full:"+u.Hostname())
	}
	return hosts
}

// NewProxyBenchmarker takes the whole config rather than the four knobs it
// needs: both call sites used to spell the same defaults out by hand, and one
// of them drifting is a benchmark that measures something else than the session.
func NewProxyBenchmarker(template *xraycfg.XrayConfig, binary string, cfg *config.Config) *ProxyBenchmarker {
	return &ProxyBenchmarker{
		template:      template,
		xrayBinary:    binary,
		concurrency:   cfg.BenchConcurrency,
		timeout:       cfg.BenchTimeout,
		allowInsecure: cfg.AllowInsecure,
		checkURLs:     cfg.CheckURLs(),
		probeHosts:    probeHosts(cfg.CheckURLs()),
	}
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
func buildProfileBenchConfig(p subscription.Profile, ports portPair, logLevel string, hosts []string) (json.RawMessage, error) {
	raw, err := xraycfg.MergeProfile(p.Raw, benchInbounds(ports), logLevel)
	if err != nil {
		return nil, err
	}
	// The probe has to go through the balancer as well; the panel's own rules
	// would otherwise send it straight out and time the plain internet.
	return xraycfg.PrependProbeRule(raw, hosts)
}

// measureOne times one server and records why it failed. The column has room
// for one word (see BenchmarkResult.String); the reason itself only exists here
// and in the log, which is where a "timeout" everywhere is diagnosed from.
func (pb *ProxyBenchmarker) measureOne(ctx context.Context, entry subscription.SubEntry, ports portPair, dir string) subscription.BenchmarkResult {
	res := pb.measureEntry(ctx, entry, ports, dir)
	if res.Error != nil && ctx.Err() == nil {
		slog.Warn("ping failed", "server", entry.Remarks, "address", entry.Address, "verdict", res.String(), "error", res.Error)
	}
	return res
}

func (pb *ProxyBenchmarker) measureEntry(ctx context.Context, entry subscription.SubEntry, ports portPair, dir string) subscription.BenchmarkResult {
	if err := entry.Validate(); err != nil {
		return subscription.BenchmarkResult{Error: err}
	}

	inbounds := benchInbounds(ports)

	// A server from a panel subscription is measured the way it will be run —
	// same dns, same routing, same outbound chain (ADR-0002). Rebuilding it on
	// template.json instead is what made new subscriptions read "timeout" while
	// connecting to them worked.
	if data, ok := pb.buildSingleBenchConfig(entry, inbounds); ok {
		return pb.runAndMeasure(ctx, data, ports, dir)
	}

	entry.AllowInsecure = pb.allowInsecure
	outboundJSON, err := subscription.ProxyOutboundJSON(&entry)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
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
		Routing:   xraycfg.AddCatchAllRule(pb.template.Routing),
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	data, err = xraycfg.PrependProbeRule(data, pb.probeHosts)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	return pb.runAndMeasure(ctx, data, ports, dir)
}

// buildSingleBenchConfig builds the session's own config for one server, pinned
// to it. False means the server has no panel config behind it (URL list, bare
// link) and the caller falls back to template.json, exactly as the session does.
func (pb *ProxyBenchmarker) buildSingleBenchConfig(entry subscription.SubEntry, inbounds []xraycfg.Inbound) (json.RawMessage, bool) {
	if len(entry.ProfileRaw) == 0 || len(entry.RawOutbound) == 0 {
		return nil, false
	}
	tag := xraycfg.OutboundTag(entry.RawOutbound)
	if tag == "" {
		return nil, false
	}
	raw, err := xraycfg.MergeProfileSingle(entry.ProfileRaw, tag, inbounds, "error")
	if err != nil {
		if !errors.Is(err, xraycfg.ErrNoPanelRouting) {
			slog.Warn("panel routing not applied to the measured config", "server", entry.Remarks, "error", err)
		}
		return nil, false
	}
	raw, err = xraycfg.PrependProbeRule(raw, pb.probeHosts)
	if err != nil {
		slog.Warn("probe rule not applied to the measured config", "server", entry.Remarks, "error", err)
		return nil, false
	}
	return raw, true
}

// measureProfile times the profile as a whole, through its own balancer.
func (pb *ProxyBenchmarker) measureProfile(ctx context.Context, p subscription.Profile, ports portPair, dir string) subscription.BenchmarkResult {
	res := pb.measureProfileEntry(ctx, p, ports, dir)
	if res.Error != nil && ctx.Err() == nil {
		slog.Warn("ping failed", "profile", p.Name, "verdict", res.String(), "error", res.Error)
	}
	return res
}

func (pb *ProxyBenchmarker) measureProfileEntry(ctx context.Context, p subscription.Profile, ports portPair, dir string) subscription.BenchmarkResult {
	// A bare link carries no panel config: it is a single server wearing a
	// profile's clothes, so measure it as one.
	if len(p.Raw) == 0 {
		if len(p.Entries) == 0 {
			return subscription.BenchmarkResult{Error: fmt.Errorf("профиль без серверов")}
		}
		return pb.measureOne(ctx, p.Entries[0], ports, dir)
	}

	data, err := buildProfileBenchConfig(p, ports, "error", pb.probeHosts)
	if err != nil {
		return subscription.BenchmarkResult{Error: err}
	}
	return pb.runAndMeasure(ctx, data, ports, dir)
}

// runAndMeasure starts xray on the given config and times a single request
// through its http inbound.
func (pb *ProxyBenchmarker) runAndMeasure(ctx context.Context, cfgJSON []byte, ports portPair, dir string) subscription.BenchmarkResult {
	tmpFile := filepath.Join(dir, fmt.Sprintf("xray-bench-%d-%d.json", ports.socks, ports.http))
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
	// Stop only signals; without the Wait the finished process stays a zombie
	// until the app exits, and one press of "b" leaves one per measured item.
	defer func() { _ = runner.Stop(); _ = runner.Wait() }()

	if !awaitPort(ctx, ports.http, "", pb.timeout) {
		return subscription.BenchmarkResult{Error: fmt.Errorf("%w: порт %d так и не открылся", subscription.ErrCoreNotReady, ports.http)}
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", ports.http)
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		},
	}
	// The keep-alive connections point at an xray we are about to kill; without
	// this they sit in the pool until the GC gets around to the transport.
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	return probe(ctx, client, pb.checkURLs, time.Now().Add(pb.timeout))
}

// probe requests the check URLs until one succeeds or the deadline passes. The
// whole list is tried on every round, not just the first entry: a server that
// blocks google (or a provider that does) would otherwise read as "timeout"
// while the link works fine. One
// shot is not enough for a profile with an observatory-driven balancer: right
// after start the observer has no measurement yet, so leastLoad matches nothing
// and the request lands in the balancer's fallbackTag ("block" in panel
// configs). A few seconds later the same profile answers — which is why it
// connected fine while the benchmark reported a timeout.
//
// The first successful request is only the warm-up: it pays DNS, the TCP dial
// and both TLS handshakes on top of the round trip, so it reads far higher than
// the link is. The number reported is the one after that, over the connection
// already in the pool — see warmProbe.
func probe(ctx context.Context, client *http.Client, checkURLs []string, deadline time.Time) subscription.BenchmarkResult {
	var lastErr error
	for {
		for _, checkURL := range checkURLs {
			latency, err := timeRequest(ctx, client, checkURL)
			if err == nil {
				return warmProbe(ctx, client, checkURL, latency)
			}
			lastErr = err
		}

		if time.Now().After(deadline) || ctx.Err() != nil {
			return subscription.BenchmarkResult{Error: lastErr}
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// warmProbe repeats the request over the connection the first one established
// and reports that timing — what the link feels like in use, and the same thing
// the connected screen shows. cold is the fallback if the repeat fails.
func warmProbe(ctx context.Context, client *http.Client, checkURL string, cold time.Duration) subscription.BenchmarkResult {
	latency, err := timeRequest(ctx, client, checkURL)
	if err != nil {
		return subscription.BenchmarkResult{Latency: cold}
	}
	return subscription.BenchmarkResult{Latency: latency}
}

// timeRequest times one GET; a non-2xx answer counts as a failure.
func timeRequest(ctx context.Context, client *http.Client, checkURL string) (time.Duration, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return time.Since(start), nil
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
	measure func(context.Context, T, portPair, string) subscription.BenchmarkResult,
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
					result = measure(ctx, items[idx], ports, dir)
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
