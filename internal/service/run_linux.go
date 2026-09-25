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

// listenerUIDs reads the owners of the sockets bound to port on 127.0.0.1 or
// 0.0.0.0 out of /proc/net/tcp (listeners) or /proc/net/udp. Every one of
// them: with SO_REUSEADDR or SO_REUSEPORT a port holds several.
func listenerUIDs(network string, port int) []int {
	state := "0A" // TCP_LISTEN
	if network == "udp" {
		state = "07" // TCP_CLOSE: a bound, unconnected datagram socket
	}
	return listenerUIDsIn("/proc/net/"+network, state, port)
}

func listenerUIDsIn(path, state string, port int) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	want := fmt.Sprintf(":%04X", port)
	var uids []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid ...
		fld := strings.Fields(sc.Text())
		if len(fld) < 8 || fld[3] != state {
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
		if uid, err := strconv.Atoi(fld[7]); err == nil {
			uids = append(uids, uid)
		}
	}
	return uids
}
