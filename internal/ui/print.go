package ui

import (
	"fmt"
	"strings"
)

func Title(text string) {
	fmt.Println()
	fmt.Printf("  %s%s%s %s\n", ColorCyan, Bold("──"), ColorReset, text)
	fmt.Printf("  %s%s%s\n", ColorCyan, strings.Repeat("─", 44), ColorReset)
}

func Item(i int, text string, tags ...string) {
	tagStr := ""
	for _, t := range tags {
		tagStr += "  " + Dim(t)
	}
	fmt.Printf("  %s%2d.%s  %s%s\n", ColorCyan, i, ColorReset, text, tagStr)
}

func Success(text string) {
	fmt.Printf("  %s✅%s %s\n", ColorGreen, ColorReset, text)
}

func Error(text string) {
	fmt.Printf("  %s❌%s %s\n", ColorRed, ColorReset, text)
}

func Warn(text string) {
	fmt.Printf("  %s⚠️%s %s\n", ColorYellow, ColorReset, text)
}

func Progress(text string) {
	fmt.Printf("  ⏳ %s...", text)
}

func ClearLine() {
	fmt.Print("\033[2K\r")
}

func Divider() {
	fmt.Printf("  %s─────────────────────────────────────────────%s\n", ColorDim, ColorReset)
}

func ClearScreen() {
	fmt.Print("\033[H\033[2J")
}
