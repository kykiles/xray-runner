package system

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// AppsFile is the split-tunnel list: one process name per line, "#" starts a
// comment. A missing file means an empty list — split tunnelling is simply off,
// not broken.
const AppsFile = "apps.txt"

// Process is one running process as shown in the picker: the executable's name
// and how many PIDs currently share it (a browser is dozens).
type Process struct {
	Name string
	PIDs int
}

// LoadApps reads the process names to route through the proxy. Blank lines and
// comments are dropped; names keep their original case (matching is done
// case-insensitively) and duplicates are collapsed.
func LoadApps(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var names []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		name := strings.TrimSpace(line)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	return names, nil
}

// SaveApps writes the list back, one name per line. The file can hold process
// names the user has not started yet, so it is written even when empty.
func SaveApps(path string, names []string) error {
	var b strings.Builder
	b.WriteString("# Процессы, чей трафик идёт через VPN в режиме proxy.\n")
	b.WriteString("# Одно имя в строке, '#' — комментарий.\n")
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}
