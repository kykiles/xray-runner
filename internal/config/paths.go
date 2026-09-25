package config

// Where the app keeps the user's state. It used to be the working directory,
// which meant a copy of the binary in Program Files could not save anything and
// the single-file build littered the folder it was double-clicked from.

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// Executable is os.Executable; a var so tests can put the program somewhere of
// their own.
var Executable = os.Executable

// ProgramDir is the directory the program itself lives in, "" when the OS will
// not say.
func ProgramDir() string {
	exe, err := Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// Path resolves a state file by name. A file already sitting next to the
// program wins: an existing install keeps working, and a copy on a flash drive
// stays portable. Everything new goes to the per-user data dir.
//
// Next to the program, not in the working directory: where the program was
// started from is no property of the install, and under sudo it is a
// directory of the user's choosing that root then reads and writes — the kind
// of path the log's G01 fix had to fence off (H02). Whoever can write next to
// the program can replace the program itself, so that directory is trusted as
// far as the program is.
func Path(name string) string {
	if dir := ProgramDir(); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if dir := DataDir(); dir != "" {
		return filepath.Join(dir, name)
	}
	if dir := ProgramDir(); dir != "" {
		return filepath.Join(dir, name)
	}
	return name
}

// EnvFile is where the settings file is read from unless -config names one:
// .env next to the program, for the reason Path looks there (H02).
func EnvFile() string {
	if dir := ProgramDir(); dir != "" {
		return filepath.Join(dir, ".env")
	}
	return ".env"
}

// DataDir is the per-user directory for state, created on first use. Empty when
// the OS won't say where it is — callers then fall back to the program's own
// directory.
func DataDir() string {
	base, err := userConfigBase()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "xray-runner")
	if ownDir(dir) != nil {
		return ""
	}
	return dir
}

// CacheDir is the per-user directory for what can be downloaded again — the
// panel's geo databases — created on first use: ~/.cache/xray-runner (the
// invoking user's under sudo) and %LOCALAPPDATA%\xray-runner on Windows. Empty
// when the OS won't say where it is. Not next to the core, where they used to
// go: a core found on PATH sits among somebody else's files (H03).
func CacheDir() string {
	base, err := userCacheBase()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "xray-runner")
	if ownDir(dir) != nil {
		return ""
	}
	return dir
}

// CacheSubdir is a directory under CacheDir, each level made the way CacheDir
// is, so a run without sudo can read and replace what a sudo run put there.
func CacheSubdir(elem ...string) (string, error) {
	dir := CacheDir()
	if dir == "" {
		return "", errors.New("папка кэша недоступна")
	}
	for _, e := range elem {
		dir = filepath.Join(dir, e)
		if err := ownDir(dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ownDir makes dir (0700) and, under sudo, hands it back to the invoking user:
// made by root, it would keep the next run without sudo from reading what is
// inside. Every directory it has to create on the way goes back too: a missing
// ~/.config made by root would otherwise stay root's, and the user's own
// programs could not write their settings there. Directories that were there
// already keep their owner. Best-effort, as everywhere else — except that a
// symlink is not followed but refused: the handback would otherwise give the
// user whatever the link pointed at, a path to root. A tampered dir is dropped
// rather than written to.
func ownDir(dir string) error {
	uid, gid, ok := sudoOwner()
	if !ok {
		return os.MkdirAll(dir, 0700)
	}
	var missing []string
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append(missing, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := chownDirToSudoUser(missing[i], uid, gid); err != nil {
			return err
		}
	}
	return chownDirToSudoUser(dir, uid, gid)
}

// LocalDir is where what belongs to this one machine is kept: the HWID — one
// per device, by definition — and the log. On Windows that is %LOCALAPPDATA%:
// %APPDATA% is Roaming, copied to every machine a domain profile signs in on,
// which would hand them all one HWID (H04). Elsewhere it is the data dir: the
// cache is for what may be thrown away, and a lost HWID is a new device to the
// panel.
func LocalDir() string {
	if goos == "windows" {
		if dir := CacheDir(); dir != "" {
			return dir
		}
	}
	return DataDir()
}

// LocalPath is Path for a file of LocalDir's: one already next to the program
// wins, as there.
func LocalPath(name string) string {
	if dir := ProgramDir(); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if dir := LocalDir(); dir != "" {
		return filepath.Join(dir, name)
	}
	return Path(name)
}

// userConfigBase is os.UserConfigDir, except that under sudo it answers for the
// user who invoked it rather than for root. Otherwise a TUN session (root) and
// a proxy session (not root) would each see their own set of subscriptions —
// and their own HWID.
func userConfigBase() (string, error) {
	if uid, _, ok := sudoOwner(); ok {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.HomeDir != "" {
			return filepath.Join(u.HomeDir, ".config"), nil
		}
	}
	return os.UserConfigDir()
}

// userCacheBase is os.UserCacheDir, answering for the user behind sudo the way
// userConfigBase does.
func userCacheBase() (string, error) {
	if uid, _, ok := sudoOwner(); ok {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && u.HomeDir != "" {
			return filepath.Join(u.HomeDir, ".cache"), nil
		}
	}
	return os.UserCacheDir()
}

// sudoOwner is the user behind a sudo invocation; absent on Windows and on a
// plain run.
func sudoOwner() (uid, gid int, ok bool) {
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil {
		return 0, 0, false
	}
	gid, err = strconv.Atoi(os.Getenv("SUDO_GID"))
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}
