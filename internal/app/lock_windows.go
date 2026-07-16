//go:build windows

package app

import "os"

// processAlive reports whether a process with the given pid is currently
// running. On Windows os.FindProcess opens a handle to the process and fails if
// no such process exists, so a successful open means the owner is still alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	proc.Release()
	return true
}
