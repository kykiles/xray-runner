package system

import (
	"os"
	"strconv"
)

// RestoreSudoOwner hands a file created under sudo back to the invoking user
// (SUDO_UID/SUDO_GID). Files the tool writes as root land root-owned 0600 and
// stay unreadable to the person who ran it — the log being the one they need.
// A no-op when not running as root or not launched via sudo.
func RestoreSudoOwner(path string) error {
	uid, gid, ok := sudoOwner()
	if !ok {
		return nil
	}
	return os.Chown(path, uid, gid)
}

// RestoreSudoOwnerFile is RestoreSudoOwner on an already open file. Handing
// ownership over by descriptor resolves nothing on disk, so it cannot be
// redirected at a symlink or raced by a swap between the open and the chown —
// the caller must prefer it whenever it holds the file.
func RestoreSudoOwnerFile(f *os.File) error {
	uid, gid, ok := sudoOwner()
	if !ok {
		return nil
	}
	return f.Chown(uid, gid)
}

// sudoOwner reports the user behind a sudo invocation, and false when there is
// nobody to hand ownership back to.
func sudoOwner() (uid, gid int, ok bool) {
	if os.Geteuid() != 0 {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return 0, 0, false // not launched via sudo; leave ownership as is
	}
	return uid, gid, true
}
