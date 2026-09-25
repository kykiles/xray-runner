//go:build !windows

package updater

import (
	"os"
	"path/filepath"
	"syscall"
)

// geteuid is os.Geteuid; a var so a test can see the owner chosen without
// being root.
var geteuid = os.Geteuid

// installOwner is who a file installed as dir/name should belong to: the owner
// of the file it replaces, or of dir when there is none yet. ok is false when
// no owner is to be set — only root can give a file away, and anyone else's
// new file is theirs already.
func installOwner(dir, name string) (uid, gid int, ok bool) {
	if geteuid() != 0 {
		return 0, 0, false
	}
	fi, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		if fi, err = os.Stat(dir); err != nil {
			return 0, 0, false
		}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
