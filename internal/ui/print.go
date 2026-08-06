package ui

import "fmt"

// The menus are drawn by internal/tui. What is left here are the two lines
// printed *between* full-screen menus, where there is no bubbletea program to
// render them.

// ClearScreen wipes the visible console before the first menu. The menus live
// in the alternate buffer, but the console shows the normal one through every
// gap between two screens — and that gap is where the shell's leftover text
// flashes past. Nothing to show through, nothing to notice. The scrollback is
// deliberately left alone (no ESC[3J): what the user ran before us is theirs,
// and on Linux erasing it on every launch is a worse bug than the flash.
// A target that cannot render escapes (colorReset blanked, see init) would
// print them as text instead.
func ClearScreen() {
	if colorReset == "" {
		return
	}
	fmt.Print("\033[2J\033[H")
}

func Error(text string) {
	fmt.Printf("  %s%s%s\n", colorRed, text, colorReset)
}

func Warn(text string) {
	fmt.Printf("  %s%s%s\n", colorYellow, text, colorReset)
}
