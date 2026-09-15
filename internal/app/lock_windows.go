//go:build windows

package app

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// processAlive reports whether a process with the given pid is currently
// running. Opening a handle is not enough: Windows keeps a pid valid while any
// handle to the dead process is still open, so an exited owner would look alive
// and a refusal would name it. The exit code is the answer —
// STILL_ACTIVE means the owner really is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// openLockFile opens the lock file for acquireLock, creating it if need be.
// Links are followed, as for the log: planting one is the unix sudo scenario,
// and creating a symlink on Windows is itself privileged.
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
}

// lockOffset is where the one locked byte lies. Windows byte-range locks are
// mandatory: a lock over the pid would keep a refused instance from reading who
// holds it. Locking past the end of the file is allowed, and the pid never
// reaches that far.
const lockOffset = 1 << 30

// tryLock takes an exclusive lock on f without waiting; errLockBusy when another
// handle holds it. Windows lets the lock go when the handle closes, in a killed
// process too.
func tryLock(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{Offset: lockOffset})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

// unlock releases the lock before the handle closes: on close alone Windows
// frees it only "depending upon available system resources" (LockFileEx docs).
func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{Offset: lockOffset})
}
