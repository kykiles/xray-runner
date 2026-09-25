package service

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"xray-runner/internal/ipc"
)

// hello opens a connection as peer and returns what the service said.
func (r *rig) hello(peer ipc.Peer) (*ipc.Client, ipc.HelloReply) {
	r.t.Helper()
	cli, srv := net.Pipe()
	r.l.ch <- ipc.NewConn(srv, peer)
	c := ipc.NewClient(cli)
	r.t.Cleanup(func() { _ = c.Close() })
	var h ipc.HelloReply
	if err := c.Call(ctxT(r.t), ipc.TypeHello, ipc.Hello{Version: ipc.Version}, &h); err != nil {
		r.t.Fatal(err)
	}
	return c, h
}

// putOver sends data to the service in pieces of piece bytes.
func putOver(t *testing.T, c *ipc.Client, data []byte, piece int) error {
	t.Helper()
	for off := 0; off < len(data); off += piece {
		end := min(off+piece, len(data))
		p := ipc.GeoPut{SHA256: shaOf(data), Size: int64(len(data)), Offset: int64(off), Data: data[off:end]}
		if err := c.Call(ctxT(t), ipc.TypeGeoPut, p, nil); err != nil {
			return err
		}
	}
	return nil
}

func newGeoRig(t *testing.T) (*rig, string) {
	dir := filepath.Join(t.TempDir(), "geo")
	return newRigWith(t, func(d *Deps) { d.GeoDir = dir }), dir
}

// An interface asks what the service lacks, sends it, and the session runs on
// the pair it sent: the core is pointed at a folder holding exactly those
// databases.
func TestServiceRunsTheInterfacesGeo(t *testing.T) {
	r, _ := newGeoRig(t)
	c, h := r.hello(alice)
	if !h.Geo {
		t.Fatal("hello does not say the service takes geo databases")
	}
	ip, site := geoDB("private", "ru"), geoDB("ru-blocked", "torrent")
	ref := ipc.GeoRef{IP: shaOf(ip), Site: shaOf(site)}

	var have ipc.GeoHaveReply
	if err := c.Call(ctxT(t), ipc.TypeGeoHave, ipc.GeoHave{SHA256: []string{ref.IP, ref.Site}}, &have); err != nil {
		t.Fatal(err)
	}
	if len(have.Missing) != 2 {
		t.Fatalf("missing %v, want both", have.Missing)
	}
	for _, d := range [][]byte{ip, site} {
		if err := putOver(t, c, d, 7); err != nil {
			t.Fatalf("geo_put: %v", err)
		}
	}
	var after ipc.GeoHaveReply
	if err := c.Call(ctxT(t), ipc.TypeGeoHave, ipc.GeoHave{SHA256: []string{ref.IP, ref.Site}}, &after); err != nil || len(after.Missing) != 0 {
		t.Fatalf("missing %v, %v after sending both", after.Missing, err)
	}

	req := tunReq()
	req.Geo = &ref
	if err := c.Call(ctxT(t), ipc.TypeStartTun, req, &ipc.StartReply{}); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	dir := r.m.lastSpec.GeoDir
	r.m.mu.Unlock()
	if dir == "" {
		t.Fatal("the session runs on the service's own databases")
	}
	for name, want := range map[string][]byte{"geoip.dat": ip, "geosite.dat": site} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
}

// A session naming a pair the service does not hold does not start.
func TestServiceRefusesUnsentGeo(t *testing.T) {
	r, _ := newGeoRig(t)
	c, _ := r.hello(alice)
	req := tunReq()
	req.Geo = &ipc.GeoRef{IP: shaOf(geoDB("a")), Site: shaOf(geoDB("b"))}
	var re *ipc.RemoteError
	if err := c.Call(ctxT(t), ipc.TypeStartTun, req, nil); !errors.As(err, &re) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if up, _ := r.m.counts(); up != 0 {
		t.Error("the session started")
	}
	// And the service is free again: one on its own databases starts.
	waitFor(t, func() bool {
		return c.Call(ctxT(t), ipc.TypeStartTun, tunReq(), &ipc.StartReply{}) == nil
	}, "the refused session still holds the service")
}

// A connection that goes halfway through a database takes the half with it.
func TestServiceDropsAHalfSentGeo(t *testing.T) {
	r, dir := newGeoRig(t)
	c, _ := r.hello(alice)
	data := geoDB("ru-blocked", "torrent")
	p := ipc.GeoPut{SHA256: shaOf(data), Size: int64(len(data)), Data: data[:4]}
	if err := c.Call(ctxT(t), ipc.TypeGeoPut, p, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	waitFor(t, func() bool {
		entries, err := os.ReadDir(filepath.Join(dir, "tmp"))
		return err == nil && len(entries) == 0
	}, "the half-sent database is still there")
	// And its place among the uploads is free: as many as the bound go in.
	for range maxGeoUploads {
		c, _ := r.hello(bob)
		if err := c.Call(ctxT(t), ipc.TypeGeoPut, p, nil); err != nil {
			t.Fatalf("upload refused: %v", err)
		}
	}
}

// A service without a store says so in its hello and refuses what an
// interface that did not listen sends anyway.
func TestServiceWithoutGeoStore(t *testing.T) {
	r := newRig(t)
	c, h := r.hello(alice)
	if h.Geo {
		t.Fatal("hello offers geo without a store")
	}
	sha := shaOf(geoDB("a"))
	if err := c.Call(ctxT(t), ipc.TypeGeoHave, ipc.GeoHave{SHA256: []string{sha}}, nil); err == nil {
		t.Error("geo_have served")
	}
	if err := c.Call(ctxT(t), ipc.TypeGeoPut, ipc.GeoPut{SHA256: sha, Size: 3, Data: []byte("abc")}, nil); err == nil {
		t.Error("geo_put served")
	}
	req := tunReq()
	req.Geo = &ipc.GeoRef{IP: sha, Site: sha}
	if err := c.Call(ctxT(t), ipc.TypeStartTun, req, nil); err == nil {
		t.Error("a session on geo the service cannot hold started")
	}
}

// A store that cannot be made leaves the service working on its own
// databases.
func TestServiceGeoStoreUnavailable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, func(d *Deps) { d.GeoDir = filepath.Join(file, "geo") })
	c, h := r.hello(alice)
	if h.Geo {
		t.Fatal("hello offers geo without a store")
	}
	if err := c.Call(ctxT(t), ipc.TypeStartTun, tunReq(), &ipc.StartReply{}); err != nil {
		t.Fatalf("a session on the service's own databases: %v", err)
	}
}
