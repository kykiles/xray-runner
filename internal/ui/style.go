package ui

const (
	ColorCyan   = "\033[36m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorDim    = "\033[2m"
	ColorReset  = "\033[0m"
)

func Bold(text string) string {
	return "\033[1m" + text + ColorReset
}

func Dim(text string) string {
	return ColorDim + text + ColorReset
}

func Colored(color, text string) string {
	return color + text + ColorReset
}
