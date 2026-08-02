//go:build windows

package app

import "syscall"

// processAlive reports whether a process with the given pid is currently
// running. Opening a handle is not enough: Windows keeps a pid valid while any
// handle to the dead process is still open, so an exited owner would look alive
// and its stale lock would never be reclaimed. The exit code is the answer —
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
