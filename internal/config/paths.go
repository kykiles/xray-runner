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

// Path resolves a state file by name. A file already sitting in the working
// directory wins: an existing install keeps working, and a copy on a flash
// drive stays portable. Everything new goes to the per-user data dir.
func Path(name string) string {
	if _, err := os.Stat(name); err == nil {
		return name
	}
	dir := DataDir()
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// DataDir is the per-user directory for state, created on first use. Empty when
// the OS won't say where it is — callers then fall back to the working
// directory, which is what the app did all along.
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
	// able to read its own subscriptions. Best-effort, as everywhere else.
	if uid, gid, ok := sudoOwner(); ok {
		_ = os.Chown(dir, uid, gid)
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
