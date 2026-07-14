package ui

import (
	"os"

	"golang.org/x/term"
)

// Colors are variables so plain mode can blank them out (U-3).
var (
	ColorCyan   = "\033[36m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorDim    = "\033[2m"
	ColorReset  = "\033[0m"

	bold = "\033[1m"

	// plain disables colors and cursor-control sequences: non-TTY output,
	// NO_COLOR, TERM=dumb, or a Windows console without VT support (U-3).
	plain bool
)

func init() {
	if !term.IsTerminal(int(os.Stdout.Fd())) ||
		os.Getenv("NO_COLOR") != "" ||
		os.Getenv("TERM") == "dumb" ||
		!enableVT() {
		setPlain()
	}
}

func setPlain() {
	plain = true
	ColorCyan, ColorGreen, ColorYellow, ColorRed, ColorDim, ColorReset, bold = "", "", "", "", "", "", ""
}

// IsPlain reports whether cursor-control output must be avoided (U-3).
func IsPlain() bool { return plain }

func Bold(text string) string {
	return bold + text + ColorReset
}

func Dim(text string) string {
	return ColorDim + text + ColorReset
}

func Colored(color, text string) string {
	return color + text + ColorReset
}
