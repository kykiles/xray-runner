//go:build linux

package service

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// runPlatform runs the service in the foreground, as systemd starts it. The
// log goes to stderr, which is the journal's.
func runPlatform(version string) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runService(ctx, version)
}

// listenerUID reads the owner of the TCP listener on 127.0.0.1:port out of
// /proc/net/tcp.
func listenerUID(port int) (int, bool) {
	return listenerUIDIn("/proc/net/tcp", port)
}

func listenerUIDIn(path string, port int) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	want := fmt.Sprintf(":%04X", port)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid ...
		fld := strings.Fields(sc.Text())
		if len(fld) < 8 || fld[3] != "0A" { // 0A: LISTEN
			continue
		}
		local := fld[1]
		if !strings.HasSuffix(local, want) {
			continue
		}
		// 127.0.0.1 or 0.0.0.0, little-endian hex.
		if addr := strings.TrimSuffix(local, want); addr != "0100007F" && addr != "00000000" {
			continue
		}
		uid, err := strconv.Atoi(fld[7])
		if err != nil {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}
