package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/ipc"
)

// geoDB is a geo database carrying the named lists, in the layout
// xraycfg.CheckGeoDatabase reads.
func geoDB(names ...string) []byte {
	var out []byte
	for _, n := range names {
		entry := append([]byte{0x0A, byte(len(n))}, n...)
		out = append(out, 0x0A, byte(len(entry)))
		out = append(out, entry...)
	}
	return out
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sendGeo puts data into the store in pieces of piece bytes, as one
// connection would, and returns the error of the last piece.
func sendGeo(g *geoStore, sha string, data []byte, piece int) error {
	var up *geoUpload
	for off := 0; off < len(data); off += piece {
		end := min(off+piece, len(data))
		var err error
		up, err = g.put(up, ipc.GeoPut{SHA256: sha, Size: int64(len(data)), Offset: int64(off), Data: data[off:end]})
		if err != nil {
			return err
		}
	}
	if up != nil {
		g.abort(up)
		return os.ErrClosed
	}
	return nil
}

func newTestGeoStore(t *testing.T) *geoStore {
	t.Helper()
	g, err := newGeoStore(filepath.Join(t.TempDir(), "geo"))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// tmpEmpty fails the test when anything was left on its way in.
func tmpEmpty(t *testing.T, g *geoStore) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(g.dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left in tmp: %v", entries)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.uploading != 0 {
		t.Errorf("%d uploads still counted", g.uploading)
	}
}

// A database that comes in whole, matching its hash, is kept by its hash, and
// is not asked for again.
func TestGeoStoreKeepsADatabase(t *testing.T) {
	g := newTestGeoStore(t)
	data := geoDB("ru-blocked", "torrent", "category-ads")
	sha := shaOf(data)
	if missing, err := g.missing([]string{sha}); err != nil || len(missing) != 1 {
		t.Fatalf("missing = %v, %v; want the database asked for", missing, err)
	}
	if err := sendGeo(g, sha, data, 5); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := os.ReadFile(g.blob(sha))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("blob = %q, %v; want the database", got, err)
	}
	if missing, err := g.missing([]string{sha}); err != nil || len(missing) != 0 {
		t.Errorf("missing = %v, %v; want nothing", missing, err)
	}
	tmpEmpty(t, g)
}

// What does not match its hash, or is not a geo database, is not kept.
func TestGeoStoreRefusesWhatItCannotTrust(t *testing.T) {
	g := newTestGeoStore(t)
	db := geoDB("ru-blocked")
	junk := []byte("#!/bin/sh\nrm -rf /\n")
	for name, tc := range map[string]struct {
		sha  string
		data []byte
		want string
	}{
		"hash":         {shaOf(geoDB("other")), db, "SHA-256"},
		"not a geo db": {shaOf(junk), junk, "не принята"},
		"truncated":    {shaOf(db[:len(db)-2]), db[:len(db)-2], "не принята"},
	} {
		t.Run(name, func(t *testing.T) {
			err := sendGeo(g, tc.sha, tc.data, 4)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			if _, err := os.Stat(g.blob(tc.sha)); err == nil {
				t.Error("kept anyway")
			}
			tmpEmpty(t, g)
		})
	}
}

// Only a hash names a file in the store, and pieces come in order, of a size
// that fits.
func TestGeoStoreRefusesBadPieces(t *testing.T) {
	g := newTestGeoStore(t)
	data := geoDB("ru-blocked", "torrent")
	sha := shaOf(data)
	size := int64(len(data))
	for name, p := range map[string]ipc.GeoPut{
		"path":       {SHA256: "../../../etc/passwd", Size: size, Data: data},
		"upper case": {SHA256: strings.ToUpper(sha), Size: size, Data: data},
		"too large":  {SHA256: sha, Size: ipc.MaxGeoFile + 1, Data: data},
		"empty":      {SHA256: sha, Size: 0},
		"past size":  {SHA256: sha, Size: 2, Data: data},
		"no start":   {SHA256: sha, Size: size, Offset: 4, Data: data[4:]},
	} {
		t.Run(name, func(t *testing.T) {
			if up, err := g.put(nil, p); err == nil || up != nil {
				t.Fatalf("put = %v, %v; want a refusal", up, err)
			}
			tmpEmpty(t, g)
		})
	}

	t.Run("gap", func(t *testing.T) {
		up, err := g.put(nil, ipc.GeoPut{SHA256: sha, Size: size, Data: data[:4]})
		if err != nil {
			t.Fatal(err)
		}
		if up, err = g.put(up, ipc.GeoPut{SHA256: sha, Size: size, Offset: 6, Data: data[6:]}); err == nil || up != nil {
			t.Fatalf("put = %v, %v; want a refusal", up, err)
		}
		tmpEmpty(t, g)
	})
}

// Only so many databases come in at once; one dropped frees its place.
func TestGeoStoreBoundsUploads(t *testing.T) {
	g := newTestGeoStore(t)
	data := geoDB("ru-blocked", "torrent")
	first := ipc.GeoPut{SHA256: shaOf(data), Size: int64(len(data)), Data: data[:4]}
	var ups []*geoUpload
	for range maxGeoUploads {
		up, err := g.put(nil, first)
		if err != nil {
			t.Fatal(err)
		}
		ups = append(ups, up)
	}
	if _, err := g.put(nil, first); err == nil {
		t.Fatal("an upload past the bound was taken")
	}
	g.abort(ups[0])
	up, err := g.put(nil, first)
	if err != nil {
		t.Fatalf("the place of a dropped upload is not free: %v", err)
	}
	g.abort(up)
	g.abort(ups[1])
	tmpEmpty(t, g)
}

// The running session's pair is a folder with the two databases under the
// names the core reads; the same pair is the same folder, and a session on
// the service's own databases leaves no pair behind.
func TestGeoStoreUse(t *testing.T) {
	g := newTestGeoStore(t)
	ip, site := geoDB("private", "ru"), geoDB("ru-blocked")
	ref := ipc.GeoRef{IP: shaOf(ip), Site: shaOf(site)}
	if _, err := g.use(&ref); err == nil {
		t.Fatal("a pair made of databases the store lacks")
	}
	for _, d := range [][]byte{ip, site} {
		if err := sendGeo(g, shaOf(d), d, 3); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := g.use(&ref)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"geoip.dat": ip, "geosite.dat": site} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if again, err := g.use(&ref); err != nil || again != dir {
		t.Errorf("use again = %q, %v; want %q", again, err, dir)
	}
	if _, err := g.use(&ipc.GeoRef{IP: "../x", Site: ref.Site}); err == nil {
		t.Error("a pair named by a path")
	}
	if own, err := g.use(nil); err != nil || own != "" || exists(dir) {
		t.Errorf("use(nil) = %q, %v, pair left %v; want none", own, err, exists(dir))
	}
	tmpEmpty(t, g)
}

// setAge sets a blob's time to ago before now.
func setAge(t *testing.T, g *geoStore, sha string, ago time.Duration) {
	t.Helper()
	when := time.Now().Add(-ago)
	if err := os.Chtimes(g.blob(sha), when, when); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A new session's pair is the only one left, and of the databases outside it
// only those that came in or were asked about lately stay.
func TestGeoStorePrune(t *testing.T) {
	g := newTestGeoStore(t)
	sha := map[string]string{}
	for _, n := range []string{"ip1", "site1", "ip2", "site2", "young", "asked"} {
		d := geoDB(n)
		sha[n] = shaOf(d)
		if err := sendGeo(g, sha[n], d, 64); err != nil {
			t.Fatal(err)
		}
	}
	old, err := g.use(&ipc.GeoRef{IP: sha["ip2"], Site: sha["site2"]})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ip1", "site1", "ip2", "site2", "asked"} {
		setAge(t, g, sha[n], 2*geoKeep)
	}
	// Asked about just now: another interface is about to start on it.
	if _, err := g.missing([]string{sha["asked"]}); err != nil {
		t.Fatal(err)
	}

	dir, err := g.use(&ipc.GeoRef{IP: sha["ip1"], Site: sha["site1"]})
	if err != nil {
		t.Fatal(err)
	}

	if !exists(filepath.Join(dir, "geosite.dat")) || exists(old) {
		t.Errorf("pairs: new %v, old %v; want only the new", exists(dir), exists(old))
	}
	for n, want := range map[string]bool{"ip1": true, "site1": true, "ip2": false, "site2": false, "young": true, "asked": true} {
		if got := exists(g.blob(sha[n])); got != want {
			t.Errorf("blob %s kept = %v, want %v", n, got, want)
		}
	}
}

// The blobs are bounded together: the oldest make room, the running
// session's never do, and what does not fit beside them is refused.
func TestGeoStoreBounded(t *testing.T) {
	g := newTestGeoStore(t)
	dbs := map[string][]byte{}
	for _, n := range []string{"aa", "bb", "cc", "dd", "ee", "ff"} {
		dbs[n] = geoDB(n)
	}
	size := int64(len(dbs["aa"]))
	old := maxGeoStore
	maxGeoStore = 3 * size
	t.Cleanup(func() { maxGeoStore = old })

	put := func(n string) error { return sendGeo(g, shaOf(dbs[n]), dbs[n], 64) }
	for i, n := range []string{"aa", "bb", "cc"} {
		if err := put(n); err != nil {
			t.Fatal(err)
		}
		setAge(t, g, shaOf(dbs[n]), time.Duration(3-i)*time.Hour) // aa oldest
	}
	if _, err := g.use(&ipc.GeoRef{IP: shaOf(dbs["aa"]), Site: shaOf(dbs["bb"])}); err != nil {
		t.Fatal(err)
	}
	// Full: dd takes the place of cc, the oldest outside the pair.
	if err := put("dd"); err != nil {
		t.Fatalf("dd: %v", err)
	}
	for n, want := range map[string]bool{"aa": true, "bb": true, "cc": false, "dd": true} {
		if got := exists(g.blob(shaOf(dbs[n]))); got != want {
			t.Errorf("blob %s kept = %v, want %v", n, got, want)
		}
	}
	// With room for the pair alone, nothing else fits.
	maxGeoStore = 2 * size
	if err := put("ee"); err == nil {
		t.Fatal("a database past the bound was kept")
	}
	if !exists(g.blob(shaOf(dbs["aa"]))) || !exists(g.blob(shaOf(dbs["bb"]))) {
		t.Error("the running session's pair went to make room")
	}
	tmpEmpty(t, g)
}

// What was on its way in when the service stopped does not outlive it.
func TestNewGeoStoreEmptiesTmp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "geo")
	if _, err := newGeoStore(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp", "geo-123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := newGeoStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tmpEmpty(t, g)
}
