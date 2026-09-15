package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
)

// configsDir is where the config viewer writes what it shows, one directory per
// subscription so configs of the same name from different panels do not collide.
// Resolved through config.Path: an existing ./configs wins, otherwise it lands
// in the data dir — an install under Program Files cannot write next to itself.
const configsDir = "configs"

// handBack gives what saveConfig creates under sudo (TUN mode) to the invoking
// user, by descriptor; a var so a test can see what it reaches without root.
var handBack = system.RestoreSudoOwnerFile

// saveConfig writes the shown config to configs/<subscription>/<name>.json,
// creating the directories if they are missing (or were deleted) and
// overwriting an older copy so the file always holds the current version.
func (a *App) saveConfig(name, text string) (string, error) {
	sub := "прочее"
	if a.nav.subIdx < len(a.nav.subs) {
		sub = a.nav.subs[a.nav.subIdx].Name
	}
	base := config.Path(configsDir)
	dir := filepath.Join(base, safeName(sub))
	path := filepath.Join(dir, safeName(name)+".json")

	// Everything from the parent of the configs dir down goes through os.Root:
	// a link planted anywhere below cannot lead outside it, so under sudo only
	// what this call created changes hands (A04).
	parent, err := os.OpenRoot(filepath.Dir(base))
	if err != nil {
		return "", fmt.Errorf("создать %s: %w", base, err)
	}
	defer func() { _ = parent.Close() }()
	configs, err := openOwnDir(parent, filepath.Base(base))
	if err != nil {
		return "", fmt.Errorf("создать %s: %w", base, err)
	}
	defer func() { _ = configs.Close() }()
	subDir, err := openOwnDir(configs, filepath.Base(dir))
	if err != nil {
		return "", fmt.Errorf("создать %s: %w", dir, err)
	}
	defer func() { _ = subDir.Close() }()
	// 0600: the config carries the server's UUID/password, so it stays readable by
	// the owner only — the user's own editor opens it fine. Ownership is handed
	// over on the staged file, before it takes the name another process could
	// swap for a link.
	if err := replaceInRoot(subDir, filepath.Base(path), []byte(text+"\n"), handBack); err != nil {
		return "", fmt.Errorf("записать %s: %w", path, err)
	}
	return path, nil
}

// openOwnDir opens name inside r, creating it (0700) if it is missing. Only a
// directory this call created is handed to the sudo user; one that was already
// there keeps whatever owner it has.
func openOwnDir(r *os.Root, name string) (*os.Root, error) {
	mkErr := r.Mkdir(name, 0o700)
	if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
		return nil, mkErr
	}
	d, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if mkErr == nil {
		if err := handBackRoot(d); err != nil {
			_ = d.Close()
			return nil, err
		}
	}
	return d, nil
}

func handBackRoot(d *os.Root) error {
	f, err := d.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return handBack(f)
}

// safeName turns a subscription or server name into one path element: anything
// that is not a letter, digit or one of -_. becomes an underscore, so a name
// with a slash, a colon or an emoji cannot escape the configs directory.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.", r) {
			return r
		}
		return '_'
	}, strings.TrimSpace(s))
	s = strings.Trim(s, "_.")
	if s == "" {
		return "config"
	}
	return s
}
