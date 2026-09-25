package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/ipc"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// geoFiles puts a geoip.dat and a geosite.dat into a folder of their own and
// returns it with their contents. The site one spans several pieces.
func geoFiles(t *testing.T) (dir string, ip, site []byte) {
	t.Helper()
	dir = t.TempDir()
	ip = []byte("geoip of the panel")
	site = bytes.Repeat([]byte("geosite of the panel "), ipc.GeoChunk/8)
	for name, b := range map[string][]byte{"geoip.dat": ip, "geosite.dat": site} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, ip, site
}

// The databases in use go to the service as far as it lacks them, in order
// and in pieces it takes, and the session names the pair.
func TestShareGeoSendsWhatTheServiceLacks(t *testing.T) {
	dir, ip, site := geoFiles(t)
	a := &App{geoDir: dir}
	f := newFakeService()
	f.hello.Geo = true
	f.geoMissing = []string{sha256Hex(site)}

	ref, err := a.shareGeo(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if ref == nil || ref.IP != sha256Hex(ip) || ref.Site != sha256Hex(site) {
		t.Fatalf("ref = %+v, want the two databases' hashes", ref)
	}
	var got []byte
	for i, p := range f.geoPuts {
		if p.SHA256 != ref.Site || p.Size != int64(len(site)) || p.Offset != int64(len(got)) || len(p.Data) > ipc.GeoChunk {
			t.Fatalf("piece %d: sha %s size %d offset %d len %d", i, p.SHA256[:8], p.Size, p.Offset, len(p.Data))
		}
		got = append(got, p.Data...)
	}
	if len(f.geoPuts) < 2 || !bytes.Equal(got, site) {
		t.Errorf("sent %d bytes in %d pieces, want geosite.dat whole and in several", len(got), len(f.geoPuts))
	}
}

// A service that does not take databases — older, or unable to keep them —
// is asked nothing and runs on its own.
func TestShareGeoWithAServiceThatTakesNone(t *testing.T) {
	dir, _, _ := geoFiles(t)
	a := &App{geoDir: dir}
	f := newFakeService()
	if ref, err := a.shareGeo(context.Background(), f); ref != nil || err != nil {
		t.Fatalf("shareGeo = %+v, %v; want nothing", ref, err)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Errorf("calls %v, want none", calls)
	}
}

// Databases that cannot be read leave the service on its own too.
func TestShareGeoWithoutReadableDatabases(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip.dat"), []byte("ip"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{geoDir: dir}
	f := newFakeService()
	f.hello.Geo = true
	if ref, err := a.shareGeo(context.Background(), f); ref != nil || err != nil {
		t.Fatalf("shareGeo = %+v, %v; want nothing", ref, err)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Errorf("calls %v, want none", calls)
	}
}

// A tun session through the service sends the databases before it starts,
// and runs on them.
func TestRemoteSessionRunsOnTheInterfacesGeo(t *testing.T) {
	a, f := newRemoteApp(t)
	dir, ip, site := geoFiles(t)
	a.geoDir = dir
	f.hello.Geo = true
	f.geoMissing = []string{sha256Hex(ip), sha256Hex(site)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.runSession(ctx, coreSessionTarget())
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(f.callList(), ipc.TypeStartTun) {
		if time.Now().After(deadline) {
			t.Fatal("no start_tun")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	calls := f.callList()
	have, put, start := slices.Index(calls, ipc.TypeGeoHave), slices.Index(calls, ipc.TypeGeoPut), slices.Index(calls, ipc.TypeStartTun)
	if have < 0 || put < have || start < put {
		t.Fatalf("calls %v, want geo_have, geo_put, then start_tun", calls)
	}
	f.mu.Lock()
	geo := f.start.Geo
	f.mu.Unlock()
	if geo == nil || geo.IP != sha256Hex(ip) || geo.Site != sha256Hex(site) {
		t.Errorf("start_tun geo = %+v, want the pair sent", geo)
	}
}

// A service that refuses the databases refuses the session with its reason.
func TestRemoteSessionGeoRefused(t *testing.T) {
	a, f := newRemoteApp(t)
	a.geoDir, _, _ = geoFiles(t)
	f.hello.Geo = true
	f.geoErr = &ipc.RemoteError{Msg: "служба уже принимает гео-базы от других подключений — повторите позже"}
	_, err := a.runSession(context.Background(), coreSessionTarget())
	var re *ipc.RemoteError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "гео-базы") {
		t.Fatalf("err = %v, want the service's refusal", err)
	}
	if slices.Contains(f.callList(), ipc.TypeStartTun) {
		t.Error("the session started without its databases")
	}
}
