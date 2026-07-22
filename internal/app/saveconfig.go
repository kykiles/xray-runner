package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"xray-runner/internal/system"
)

// configsDir is where the config viewer writes what it shows, one directory per
// subscription so configs of the same name from different panels do not collide.
const configsDir = "configs"

// saveConfig writes the shown config to configs/<subscription>/<name>.json,
// creating the directories if they are missing (or were deleted) and
// overwriting an older copy so the file always holds the current version.
func (a *App) saveConfig(name, text string) (string, error) {
	sub := "прочее"
	if a.nav.subIdx < len(a.nav.subs) {
		sub = a.nav.subs[a.nav.subIdx].Name
	}
	dir := filepath.Join(configsDir, safeName(sub))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("создать %s: %w", dir, err)
	}
	// Readable by the user's editor: the config carries the server's UUID/password,
	// but it is a file the user asked for and wants to open. Ownership is handed
	// back when the tool runs under sudo (TUN mode).
	path := filepath.Join(dir, safeName(name)+".json")
	if err := os.WriteFile(path, []byte(text+"\n"), 0644); err != nil {
		return "", fmt.Errorf("записать %s: %w", path, err)
	}
	_ = system.RestoreSudoOwner(dir)
	_ = system.RestoreSudoOwner(path)
	return path, nil
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
