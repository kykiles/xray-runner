//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ANSI escape processing in the Windows console (U-3);
// false means the console can't render escapes and plain mode must be used.
func enableVT() bool {
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
