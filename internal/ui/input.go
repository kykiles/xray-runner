package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrInterrupted is returned by ReadKey when the user presses Ctrl+C, so the
// caller can unwind to main for a single clean exit instead of os.Exit (R-4).
var ErrInterrupted = fmt.Errorf("interrupted")

func StyledInput(prompt string) (string, error) {
	fmt.Printf("  %s▸%s %s: ", ColorCyan, ColorReset, prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func ReadKey() (string, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, old)

	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil {
		return "", err
	}
	if b[0] == '\r' {
		b[0] = '\n'
	}
	if b[0] == 0x03 {
		return "", ErrInterrupted
	}
	return string(b[0]), nil
}
