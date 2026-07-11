### Task 1: Create `internal/ui/` package

**Files:**
- Create: `internal/ui/style.go`
- Create: `internal/ui/print.go`
- Create: `internal/ui/input.go`

**Interfaces:**
- Produces: `ui.Bold(text)`, `ui.Dim(text)`, `ui.Colored(color, text)`, `ui.Title(text)`, `ui.Item(i, text, tags...)`, `ui.Success(text)`, `ui.Error(text)`, `ui.Warn(text)`, `ui.Progress(text)`, `ui.ClearLine()`, `ui.Divider()`, `ui.StyledInput(prompt) (string, error)`

- [ ] **Step 1: Create `internal/ui/style.go`**

```go
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
```

- [ ] **Step 2: Create `internal/ui/print.go`**

```go
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
```

- [ ] **Step 3: Create `internal/ui/input.go`**

```go
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
```

- [ ] **Step 4: Verify compilation**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go build ./internal/ui/
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/
git commit -m "feat: add ANSI-styled UI package (internal/ui)"
```
