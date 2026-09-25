//go:build !linux

package netcap

import "os/exec"

// Delegated is Linux only: elsewhere the network is changed with elevation,
// and nothing is handed down.
func Delegated() bool { return false }

// Ambient is nil outside Linux.
func Ambient() []uintptr { return nil }

// Prepare has nothing to hand down outside Linux.
func Prepare(*exec.Cmd) {}
