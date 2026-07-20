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
	if os.Geteuid() != 0 {
		return nil
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return nil // not launched via sudo; leave ownership as is
	}
	return os.Chown(path, uid, gid)
}
