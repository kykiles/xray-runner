package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

func TestFreePortPair(t *testing.T) {
	p, err := freePortPair()
	if err != nil {
		t.Fatalf("freePortPair: %v", err)
	}
	if p.socks == 0 || p.http == 0 {
		t.Errorf("expected non-zero ports, got (%d, %d)", p.socks, p.http)
	}
	if p.socks == p.http {
		t.Errorf("expected distinct ports, got %d twice", p.socks)
	}
	// The reported ports must actually be bindable (i.e. were released).
	for _, port := range []int{p.socks, p.http} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("port %d not free after freePortPair: %v", port, err)
			continue
		}
		ln.Close()
	}
}

func TestWaitPortReachable(t *testing.T) {
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

	port := ln.Addr().(*net.TCPAddr).Port
	if !awaitPort(context.Background(), port, "", 500*time.Millisecond) {
		t.Error("expected true for open port")
	}
}

func TestWaitPortUnreachable(t *testing.T) {
	if awaitPort(context.Background(), 19999, "", 100*time.Millisecond) {
		t.Error("expected false for closed port")
	}
}

func TestWaitPortContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if awaitPort(ctx, 19999, "", 5*time.Second) {
		t.Error("expected false when context is already canceled")
	}
}

func TestProxyBenchmarkerMeasureOne(t *testing.T) {
	xrayBin, err := xray.FindBinary()
	if err != nil {
		t.Skip("xray binary not found, skipping")
	}

	tc, err := xraycfg.LoadTemplate("../../template.json")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	// The template's routing references geosite/geoip assets; skip when this
	// machine's xray can't load them (e.g. missing *.dat), so the integration
	// test only runs where the environment can actually serve the config.
	if !xrayCanRunTemplate(t, xrayBin, tc) {
		t.Skip("xray cannot run the routing template here (missing geodata?), skipping")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})}
	go hs.Serve(ln)
	defer hs.Close()

	addr := ln.Addr().(*net.TCPAddr)
	entry := subscription.SubEntry{
		Protocol: "vless",
		Address:  addr.IP.String(),
		Port:     addr.Port,
		UUID:     "550e8400-e29b-41d4-a716-446655440000",
		Network:  "tcp",
	}

	pb := NewProxyBenchmarker(tc, xrayBin, 1, 8*time.Second, false)
	result := pb.measureOne(context.Background(), entry, portPair{socks: 10850, http: 10860}, t.TempDir())

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", result.Latency)
	}
}

// xrayCanRunTemplate reports whether the local xray can validate a config
// derived from the template (which pulls in geodata assets).
func xrayCanRunTemplate(t *testing.T, xrayBin string, tc *xraycfg.XrayConfig) bool {
	t.Helper()
	cfg := &xraycfg.XrayConfig{
		Log:       &xraycfg.LogConfig{Loglevel: "error"},
		DNS:       tc.DNS,
		Inbounds:  []xraycfg.Inbound{{Tag: "http", Port: 10870, Listen: "127.0.0.1", Protocol: "http", Settings: []byte(`{"allowTransparent":false}`)}},
		Outbounds: append([]json.RawMessage{[]byte(`{"tag":"proxy","protocol":"freedom"}`)}, tc.Outbounds...),
		Routing:   xraycfg.AddCatchAllRule(tc.Routing),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false
	}
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return false
	}
	return xray.New(xrayBin, path).TestConfig(context.Background()) == nil
}
