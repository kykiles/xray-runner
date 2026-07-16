//go:build !windows

package app

import (
	"errors"
	"os"
	"syscall"
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
