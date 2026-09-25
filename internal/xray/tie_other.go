//go:build !linux && !windows

package xray

import (
	"os"
	"os/exec"
)

// No parent-death hook here: the tool ships for Linux and Windows.
func prepareChild(*exec.Cmd) {}

func adoptChild(*os.Process) {}
