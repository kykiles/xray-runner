// Package bundle carries the xray core and its data files inside our own binary
// so a release can be a single executable. The assets are embedded only in a
// build tagged `bundle` (see embed.go); an ordinary build keeps the old layout
// where everything sits next to the app.
package bundle

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// assets holds the embedded files; nil in a build without the `bundle` tag.
var assets fs.FS

// Dir unpacks the embedded assets into a cache directory and returns it, or ""
// when nothing is embedded. The directory is named after the content hash, so a
// new build unpacks once and every later run just finds it.
func Dir() (string, error) {
	if assets == nil {
		return "", nil
	}

	sum, err := hashAssets(assets)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "xray-runner", "core-"+sum)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	// Unpack aside and rename, so a run interrupted halfway does not leave a
	// truncated core behind that the next run would happily execute.
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := unpack(assets, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another process won the race and created the directory first.
		if _, statErr := os.Stat(dir); statErr == nil {
			_ = os.RemoveAll(tmp)
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// unpack writes every file of fsys into dir, decompressing the .gz ones and
// dropping that suffix.
func unpack(fsys fs.FS, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		var r io.Reader = src
		name := filepath.Base(path)
		if strings.HasSuffix(name, ".gz") {
			zr, err := gzip.NewReader(src)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			defer zr.Close()
			r, name = zr, strings.TrimSuffix(name, ".gz")
		}

		// The core has to be executable; the data files are read alongside it.
		dst, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		defer dst.Close()
		if _, err := io.Copy(dst, r); err != nil {
			return err
		}
		return dst.Close()
	})
}

// hashAssets identifies the embedded set by its content: names and bytes.
func hashAssets(fsys fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, err := io.WriteString(h, path); err != nil {
			return err
		}
		f, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}
