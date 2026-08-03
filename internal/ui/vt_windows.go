//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// The Windows console decodes our output in the OEM code page (866 on a Russian
// system) unless told otherwise, while Go writes UTF-8 — which is why every
// Cyrillic letter and every box-drawing glyph arrived as "?". Setting the output
// code page fixes the decoding for every console, old conhost included. It does
// not conjure glyphs the console font lacks: colour emoji stay blank boxes in
// conhost, and flags are drawn as letter pairs by Segoe UI Emoji on Windows by
// design. init rather than enableVT: the latter is skipped when output is not a
// terminal, and the code page has to be set either way.
func init() {
	// 65001 = CP_UTF8; x/sys/windows has no constant for it.
	_ = windows.SetConsoleOutputCP(65001)
}

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
