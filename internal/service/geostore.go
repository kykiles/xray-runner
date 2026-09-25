package service

// The geo databases a tun session runs on come from its interface. The ones a
// subscription brings sit in the user's cache, out of the service's reach, and
// those updated by `u` beside the program would reach the service's folder
// only with the next install: a panel's rules naming lists only its own
// databases carry could not run through the service at all. So the interface
// hands over the pair it checked the config against, and the service keeps
// each database by its SHA-256 in a folder no user writes, checks it is one,
// and runs the session's core on the pair.
//
// Under the store's folder:
//   - blobs/<sha256> — a database that came in whole, matched its hash and
//     read as a geo database;
//   - sets/<ip sha256>-<site sha256>/ — the running session's pair, geoip.dat
//     and geosite.dat linked to their blobs: the folder the core is pointed
//     at. Made only by the session that holds the service, so there is one;
//   - tmp/ — what is on its way in; emptied when the service starts.
//
// All of it is bounded: maxGeoStore for the blobs, the oldest going first but
// never the running session's, and maxGeoUploads databases coming in at once.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"xray-runner/internal/ipc"
	"xray-runner/internal/xraycfg"
)

// maxGeoUploads bounds the databases coming in at once, across connections:
// each is a file of up to ipc.MaxGeoFile on the service's disk until it is
// whole.
const maxGeoUploads = 2

// geoKeep is how long a database nobody runs on survives pruning after it came
// in or was asked about: another interface may be about to start on it.
const geoKeep = 10 * time.Minute

// maxGeoStore bounds the blobs together: room for a few pairs, whatever the
// interfaces send. A variable so tests need not fill half a gigabyte.
var maxGeoStore int64 = 4 * ipc.MaxGeoFile

type geoStore struct {
	dir string

	// mu orders what changes the folder: a database kept, a pair made, the
	// pruning.
	mu sync.Mutex
	// uploading counts the uploads in progress, up to maxGeoUploads.
	uploading int
	// active is the running session's pair folder — or the last one's, kept
	// for its reconnect — "" for none.
	active string
}

// newGeoStore opens the store in dir, making it when it is not there.
func newGeoStore(dir string) (*geoStore, error) {
	for _, sub := range []string{"blobs", "sets", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	// What was on its way in when the service last stopped is of no use.
	tmp := filepath.Join(dir, "tmp")
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(tmp, e.Name()))
	}
	return &geoStore{dir: dir}, nil
}

// validSHA accepts a SHA-256 in lowercase hex, which is all a name in the
// store is made of: nothing else from a request reaches a path.
func validSHA(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (g *geoStore) blob(sha string) string { return filepath.Join(g.dir, "blobs", sha) }

// missing reports which of the databases the store lacks. The ones it has
// are marked as just asked about, so pruning leaves them for geoKeep.
func (g *geoStore) missing(shas []string) ([]string, error) {
	if len(shas) > 2 {
		return nil, errors.New("гео-баз в запросе больше двух")
	}
	for _, h := range shas {
		if !validSHA(h) {
			return nil, fmt.Errorf("хеш гео-базы %q не годится", h)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	now := time.Now()
	for _, h := range shas {
		if _, err := os.Stat(g.blob(h)); err != nil {
			out = append(out, h)
			continue
		}
		_ = os.Chtimes(g.blob(h), now, now)
	}
	return out, nil
}

// geoUpload is a database on its way in over one connection.
type geoUpload struct {
	sha       string
	size, off int64
	f         *os.File
	h         hash.Hash
}

// put takes one piece of a database. cur is the connection's upload in
// progress, nil for none, and put returns the one in progress after the
// piece: nil once the database is whole and kept, or on any error — a piece
// out of order drops what came before it.
func (g *geoStore) put(cur *geoUpload, p ipc.GeoPut) (*geoUpload, error) {
	if p.Offset == 0 {
		g.abort(cur)
		cur = nil
		if !validSHA(p.SHA256) {
			return nil, fmt.Errorf("хеш гео-базы %q не годится", p.SHA256)
		}
		if p.Size <= 0 || p.Size > ipc.MaxGeoFile {
			return nil, fmt.Errorf("гео-база в %d байт: служба принимает до %d", p.Size, ipc.MaxGeoFile)
		}
		var err error
		if cur, err = g.begin(p.SHA256, p.Size); err != nil {
			return nil, err
		}
	}
	if cur == nil || p.SHA256 != cur.sha || p.Size != cur.size || p.Offset != cur.off {
		g.abort(cur)
		return nil, errors.New("кусок гео-базы пришёл не по порядку")
	}
	if len(p.Data) == 0 || len(p.Data) > ipc.GeoChunk || int64(len(p.Data)) > cur.size-cur.off {
		g.abort(cur)
		return nil, errors.New("кусок гео-базы не того размера")
	}
	if _, err := cur.f.Write(p.Data); err != nil {
		g.abort(cur)
		return nil, err
	}
	cur.h.Write(p.Data)
	cur.off += int64(len(p.Data))
	if cur.off < cur.size {
		return cur, nil
	}
	return nil, g.keep(cur)
}

// begin starts an upload, when fewer than maxGeoUploads are in progress.
func (g *geoStore) begin(sha string, size int64) (*geoUpload, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.uploading >= maxGeoUploads {
		return nil, errors.New("служба уже принимает гео-базы от других подключений — повторите позже")
	}
	if !g.room(size) {
		return nil, errGeoFull
	}
	f, err := os.CreateTemp(filepath.Join(g.dir, "tmp"), "geo-*")
	if err != nil {
		return nil, err
	}
	g.uploading++
	return &geoUpload{sha: sha, size: size, f: f, h: sha256.New()}, nil
}

// abort drops an upload in progress; nil is none.
func (g *geoStore) abort(u *geoUpload) {
	if u == nil {
		return
	}
	_ = u.f.Close()
	_ = os.Remove(u.f.Name())
	g.release()
}

func (g *geoStore) release() {
	g.mu.Lock()
	g.uploading--
	g.mu.Unlock()
}

// keep checks a database that has come in whole and puts it among the blobs.
func (g *geoStore) keep(u *geoUpload) error {
	defer g.release()
	name := u.f.Name()
	defer func() { _ = os.Remove(name) }() // nothing left there once renamed
	if err := u.f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(u.h.Sum(nil)); got != u.sha {
		return fmt.Errorf("гео-база пришла с SHA-256 %s, а заявлена %s", got, u.sha)
	}
	if err := xraycfg.CheckGeoDatabase(name); err != nil {
		return fmt.Errorf("гео-база не принята: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := os.Stat(g.blob(u.sha)); err == nil {
		// The same bytes came in over another connection meanwhile; the one
		// there may be linked into a running session's pair.
		return nil
	}
	// Others may have come in since this one began.
	if !g.room(u.size) {
		return errGeoFull
	}
	return os.Rename(name, g.blob(u.sha))
}

// errGeoFull is a database the store has no room for even with everything
// but the running session's pair gone.
var errGeoFull = errors.New("у службы нет места для гео-баз — повторите, когда текущая сессия закончится")

// pinned are the databases of the active pair, which nothing removes.
func (g *geoStore) pinned() map[string]bool {
	if g.active == "" {
		return nil
	}
	ip, site, _ := strings.Cut(filepath.Base(g.active), "-")
	return map[string]bool{ip: true, site: true}
}

// room makes space for need more bytes among the blobs, the oldest going
// first, and reports whether there is. With mu held.
func (g *geoStore) room(need int64) bool {
	type blob struct {
		path string
		size int64
		mod  time.Time
	}
	var total int64
	var spare []blob
	pinned := g.pinned()
	entries, _ := os.ReadDir(filepath.Join(g.dir, "blobs"))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		if !pinned[e.Name()] {
			spare = append(spare, blob{g.blob(e.Name()), info.Size(), info.ModTime()})
		}
	}
	slices.SortFunc(spare, func(a, b blob) int { return a.mod.Compare(b.mod) })
	for _, b := range spare {
		if total+need <= maxGeoStore {
			break
		}
		if err := os.Remove(b.path); err != nil {
			slog.Warn("служба: старая гео-база не удалена", "file", filepath.Base(b.path), "error", err)
			continue
		}
		total -= b.size
	}
	return total+need <= maxGeoStore
}

// use makes the pair the running session's: the folder its core reads the
// two databases from, returned, and the only pair left. nil is a session on
// the service's own databases: no pair at all then. Called by the session
// that holds the service, once it does.
func (g *geoStore) use(ref *ipc.GeoRef) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	dir := ""
	if ref != nil {
		var err error
		if dir, err = g.makeSet(*ref); err != nil {
			return "", err
		}
	}
	g.active = dir
	g.prune()
	return dir, nil
}

// makeSet makes the pair's folder, or finds it made. With mu held.
func (g *geoStore) makeSet(ref ipc.GeoRef) (string, error) {
	if !validSHA(ref.IP) || !validSHA(ref.Site) {
		return "", errors.New("хеш гео-базы не годится")
	}
	dir := filepath.Join(g.dir, "sets", ref.IP+"-"+ref.Site)
	// A pair is made whole elsewhere and renamed in: one that is there is
	// complete.
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	for _, h := range []string{ref.IP, ref.Site} {
		if _, err := os.Stat(g.blob(h)); err != nil {
			return "", fmt.Errorf("гео-базы %s… у службы нет: программа её не передала", h[:12])
		}
	}
	tmp, err := os.MkdirTemp(filepath.Join(g.dir, "tmp"), "set-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // gone once renamed
	for name, h := range map[string]string{"geoip.dat": ref.IP, "geosite.dat": ref.Site} {
		if err := linkOrCopy(g.blob(h), filepath.Join(tmp, name)); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// linkOrCopy puts src at dst: a hard link, or a copy where the file system
// takes none.
func linkOrCopy(src, dst string) error {
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// prune removes what no session is going to run on: every pair but the
// active one, and every database outside it that nobody sent or asked about
// for geoKeep. With mu held.
func (g *geoStore) prune() {
	sets := filepath.Join(g.dir, "sets")
	entries, _ := os.ReadDir(sets)
	for _, e := range entries {
		dir := filepath.Join(sets, e.Name())
		if dir == g.active {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("служба: старые гео-базы не удалены", "dir", dir, "error", err)
		}
	}
	pinned := g.pinned()
	blobs := filepath.Join(g.dir, "blobs")
	entries, _ = os.ReadDir(blobs)
	for _, e := range entries {
		if pinned[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < geoKeep {
			continue
		}
		if err := os.Remove(filepath.Join(blobs, e.Name())); err != nil {
			slog.Warn("служба: старая гео-база не удалена", "file", e.Name(), "error", err)
		}
	}
}
