package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"xray-runner/internal/ipc"
)

// shareGeo hands the service the geo databases in use here — the panel's, from
// the user's cache, or the ones beside the core — and names them for the
// session: the service keeps its own copy, sent once per content, and runs the
// core on it. Without that a panel's rules naming lists only its databases
// carry could not run through the service at all.
//
// nil, and no error, leaves the service on the databases it was installed
// with: a service that takes none (older, or unable to keep them), or
// databases here that cannot be read or are too large to send. A config that
// names lists those lack is refused by the service, which says why.
func (a *App) shareGeo(ctx context.Context, c serviceConn) (*ipc.GeoRef, error) {
	if c == nil || !c.Hello().Geo || a.geoDir == "" {
		return nil, nil
	}
	type file struct {
		path string
		sha  string
		size int64
	}
	var files [2]file
	for i, name := range []string{"geoip.dat", "geosite.dat"} {
		path := filepath.Join(a.geoDir, name)
		sha, size, err := hashGeo(path)
		if err != nil {
			slog.Warn("гео-базы не переданы службе, она работает на своих", "file", path, "error", err)
			return nil, nil
		}
		files[i] = file{path, sha, size}
	}
	ref := &ipc.GeoRef{IP: files[0].sha, Site: files[1].sha}

	var have ipc.GeoHaveReply
	if err := c.Call(ctx, ipc.TypeGeoHave, ipc.GeoHave{SHA256: []string{ref.IP, ref.Site}}, &have); err != nil {
		return nil, fmt.Errorf("служба: гео-базы: %w", err)
	}
	for _, sha := range have.Missing {
		for _, f := range files {
			if f.sha != sha {
				continue
			}
			slog.Info("передаю службе гео-базу", "file", f.path, "size", f.size)
			if err := putGeo(ctx, c, f.path, f.sha, f.size); err != nil {
				return nil, fmt.Errorf("гео-база %s не передана службе: %w", filepath.Base(f.path), err)
			}
			break
		}
	}
	return ref, nil
}

// errGeoTooLarge is a database over what the service takes.
var errGeoTooLarge = errors.New("гео-база больше, чем принимает служба")

// hashGeo reads a database's SHA-256 and size.
func hashGeo(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, ipc.MaxGeoFile+1))
	if err != nil {
		return "", 0, err
	}
	if n == 0 || n > ipc.MaxGeoFile {
		return "", 0, errGeoTooLarge
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// putGeo sends a database to the service piece by piece. A file that changed
// since it was hashed fails the service's check at the end.
func putGeo(ctx context.Context, c serviceConn, path, sha string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, ipc.GeoChunk)
	for off := int64(0); off < size; {
		n, err := io.ReadFull(f, buf[:min(int64(len(buf)), size-off)])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, ipc.TypeGeoPut, ipc.GeoPut{SHA256: sha, Size: size, Offset: off, Data: buf[:n]}, nil); err != nil {
			return err
		}
		off += int64(n)
	}
	return nil
}
