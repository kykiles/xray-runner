//go:build windows

package config

// chownDirToSudoUser has no callers on Windows: sudoOwner reads SUDO_UID, which
// no Windows launch sets, so DataDir never reaches the ownership handback. It
// exists only so DataDir compiles on both.
func chownDirToSudoUser(string, int, int) error { return nil }
