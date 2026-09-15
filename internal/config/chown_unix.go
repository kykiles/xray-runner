//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// chownDirToSudoUser hands the data dir back to the invoking user by descriptor,
// refusing a symlink at the final component. os.Chown follows a symlink, so a
// data dir the unprivileged user had swapped for a link to, say, /etc/cron.d
// would have been handed to them — a path to root. O_NOFOLLOW makes the open
// fail on such a link instead; O_DIRECTORY rejects anything but a directory.
func chownDirToSudoUser(dir string, uid, gid int) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open data dir: %w", err)
	}
	defer f.Close()
	return f.Chown(uid, gid)
}
