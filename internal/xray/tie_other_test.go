//go:build !linux && !windows

package xray

func coreGone(int) bool { return true }
