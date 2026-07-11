package app

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"xray-runner/internal/subscription"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

func TestPortAllocator(t *testing.T) {
	a := newPortAllocator(10810)
	p0 := a.alloc()
	p1 := a.alloc()

	if p0.socks != 10810 || p0.http != 10910 {
		t.Errorf("first alloc: expected (10810, 10910), got (%d, %d)", p0.socks, p0.http)
	}
	if p1.socks != 10811 || p1.http != 10911 {
		t.Errorf("second alloc: expected (10811, 10911), got (%d, %d)", p1.socks, p1.http)
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
	if !waitPort(port, 500*time.Millisecond) {
		t.Error("expected true for open port")
	}
}

func TestWaitPortUnreachable(t *testing.T) {
	if waitPort(19999, 100*time.Millisecond) {
		t.Error("expected false for closed port")
	}
}

func TestProxyBenchmarkerMeasureOne(t *testing.T) {
	xrayBin := xray.FindBinary()
	if _, err := os.Stat(xrayBin); err != nil {
		t.Skip("xray binary not found, skipping")
	}

	tc, err := xraycfg.LoadTemplate("../../template.json")
	if err != nil {
		t.Fatalf("load template: %v", err)
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

	pb := NewProxyBenchmarker(tc, xrayBin, 1, 8*time.Second)
	result := pb.measureOne(entry, portPair{socks: 10850, http: 10860})

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Latency <= 0 {
		t.Errorf("expected positive latency, got %v", result.Latency)
	}
}
