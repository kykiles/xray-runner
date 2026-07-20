//go:build !windows

package log

import "syscall"

// openNoFollow makes the open fail on a symlink instead of following it. The
// tool runs as root in TUN mode while the log path lives in the user's working
// directory, so a planted symlink would otherwise let any local process pick
// the file root truncates — and, through the ownership handback, take it over.
const openNoFollow = syscall.O_NOFOLLOW
