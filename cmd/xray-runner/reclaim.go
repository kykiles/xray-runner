package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

// safeToReclaim guards against walking somewhere enormous. The app keeps its
// files next to the binary and is launched from there, so a working directory
// that is the home directory or a filesystem root means the launch was unusual
// and a recursive chown is not what the user asked for.
func safeToReclaim(dir string) bool {
	if dir == "" || dir == "/" || dir == filepath.Dir(dir) {
		return false
	}
	if home := os.Getenv("HOME"); home != "" && dir == home {
		return false
	}
	return true
}

// reclaimFiles restores ownership of the working directory to the sudo user.
// It runs at startup (clearing what an earlier sudo run left behind) as well as
// at exit, and stays silent throughout: this is a convenience, and a session
// must not fail because one chown did not take.
func reclaimFiles() {
	uid, gid, ok := sudoOwner()
	if !ok {
		return
	}
	dir, err := os.Getwd()
	if err != nil || !safeToReclaim(dir) {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// Lchown, not Chown: a symlink here should change hands itself rather
		// than redirect the call at whatever it points to.
		_ = os.Lchown(path, uid, gid)
		return nil
	})
}
