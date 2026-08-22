package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"xray-runner/internal/config"
	"xray-runner/internal/system"
)

// Running under sudo makes every file the app writes — apps.txt, the generated
// xray config, the log — owned by root with mode 0600, and the next run without
// sudo then fails to read its own data. Since split tunnelling now works either
// way (root, or CAP_NET_ADMIN via setcap), both launch styles have to stay
// interchangeable, so a sudo run hands its files back to the invoking user.

// sudoOwner is the user behind a sudo invocation. Absent when the process was
// not started through sudo, which is the case this whole file exists to avoid.
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

// reclaimedFiles are the state files the app writes, by name; config.Path finds
// each one wherever it lives (next to the binary for an older install, in the
// data dir otherwise).
var reclaimedFiles = []string{
	system.AppsFile,
	"subscriptions.txt",
	"last_server.json",
	"hwid.txt",
	"xray_config.json",
	"xray_config.json.lock",
	"xray-runner.log",
}

// reclaimedDirs are the directories the app creates whole, walked recursively.
// The data dir is ours by construction; configs/ and keys/ are written into the
// working directory by the config viewer and by -dump-links.
func reclaimedDirs() []string {
	return []string{config.DataDir(), "configs", "keys"}
}

// reclaimFiles restores ownership of the app's own files to the sudo user.
// It runs at startup (clearing what an earlier sudo run left behind) as well as
// at exit, and stays silent throughout: this is a convenience, and a session
// must not fail because one chown did not take.
//
// Only files the app itself writes are touched. An earlier version walked the
// working directory, which hands over whatever tree the user happened to launch
// from — /etc for a binary on PATH — and that is a privilege escalation, not a
// convenience.
func reclaimFiles() {
	uid, gid, ok := sudoOwner()
	if !ok {
		return
	}
	for _, name := range reclaimedFiles {
		// Lchown, not Chown: a symlink here should change hands itself rather
		// than redirect the call at whatever it points to.
		_ = os.Lchown(config.Path(name), uid, gid)
	}
	for _, dir := range reclaimedDirs() {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			_ = os.Lchown(path, uid, gid)
			return nil
		})
	}
}
