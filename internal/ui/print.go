package ui

import "fmt"

// The menus are drawn by internal/tui. What is left here are the two lines
// printed *between* full-screen menus, where there is no bubbletea program to
// render them.

func Error(text string) {
	fmt.Printf("  %s❌%s %s\n", colorRed, colorReset, text)
}

func Warn(text string) {
	fmt.Printf("  %s⚠️%s %s\n", colorYellow, colorReset, text)
}
