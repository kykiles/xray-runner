package ui

import (
	"os"

	"golang.org/x/term"
)

// Colors are variables so plain mode can blank them out (U-3).
var (
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorReset  = "\033[0m"
)

func init() {
	// Blank the escapes out for a target that can't render them: non-TTY output,
	// NO_COLOR, TERM=dumb, or a Windows console without VT support (U-3).
	if !term.IsTerminal(int(os.Stdout.Fd())) ||
		os.Getenv("NO_COLOR") != "" ||
		os.Getenv("TERM") == "dumb" ||
		!enableVT() {
		colorYellow, colorRed, colorReset = "", "", ""
	}
}
