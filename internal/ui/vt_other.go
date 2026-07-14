//go:build !windows

package ui

// enableVT is a no-op outside Windows: ANSI is native there.
func enableVT() bool { return true }
