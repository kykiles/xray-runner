package config

// Where the app keeps the user's state. It used to be the working directory,
// which meant a copy of the binary in Program Files could not save anything and
// the single-file build littered the folder it was double-clicked from.

import (
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	// Created by root under sudo, and the next run without sudo would not be
	// able to read its own subscriptions, so it is handed back to the invoking
	// user. Best-effort, as everywhere else — except that a symlink at the final
	// component is not followed but refused: the handback would otherwise give
	// the user whatever the link pointed at, a path to root. A tampered data dir
	// is dropped rather than written to.
	if uid, gid, ok := sudoOwner(); ok {
		if err := chownDirToSudoUser(dir, uid, gid); err != nil {
			return ""
		}
	}
	return dir
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
