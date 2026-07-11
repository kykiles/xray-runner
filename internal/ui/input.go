package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func StyledInput(prompt string) (string, error) {
	fmt.Printf("  %s▸%s %s: ", ColorCyan, ColorReset, prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}
