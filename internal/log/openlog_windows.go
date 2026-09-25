package log

import "os"

// openLog opens this run's log, emptied. Windows has no O_NOFOLLOW, and needs
// none: the attack it guards against on unix is a root process opening a
// user-writable path and handing the file over, which is the sudo scenario;
// creating symlinks on Windows is itself privileged.
func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
}
