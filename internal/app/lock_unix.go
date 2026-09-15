//go:build !windows

package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// processAlive reports whether a process with the given pid is currently
// running. On Unix os.FindProcess always succeeds, so liveness is probed with
// signal 0: a nil error means the process exists, and ErrPermission means it
// exists but is owned by another user — both count as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// openLockFile opens the lock file for acquireLock, creating it if need be. In
// TUN mode root opens it in the user's own directory and writes into what it
// opened: a planted symlink would point that write, and the create, anywhere,
// and a hard link would carry the write to the file's other name. So a symlink
// is not followed, and a file with other names is refused.
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
			err = fmt.Errorf("%s: у файла есть другие имена (жёстких ссылок: %d)", path, st.Nlink)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// tryLock takes an exclusive flock on f without waiting; errLockBusy when
// another open file holds it. A flock belongs to the open file, not to the path
// or the pid: the kernel drops it when the last descriptor closes, in a killed
// process too.
func tryLock(f *os.File) error { return flock(f, unix.LOCK_EX|unix.LOCK_NB) }

func unlock(f *os.File) error { return flock(f, unix.LOCK_UN) }

func flock(f *os.File, how int) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	if err := conn.Control(func(fd uintptr) { lockErr = unix.Flock(int(fd), how) }); err != nil {
		return err
	}
	if errors.Is(lockErr, unix.EWOULDBLOCK) {
		return errLockBusy
	}
	return lockErr
}
