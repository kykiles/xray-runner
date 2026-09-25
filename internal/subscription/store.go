package subscription

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"

	"xray-runner/internal/config"
	"xray-runner/internal/safefile"
)

type NamedSubscription struct {
	URL  string
	Name string
}

// subscriptionsFile overrides where the list lives; empty means "resolve it",
// which is what every run does. Tests set it to a temp file.
var subscriptionsFile = ""

// subsPath is the resolved location of the subscription list.
func subsPath() string {
	if subscriptionsFile != "" {
		return subscriptionsFile
	}
	return config.Path("subscriptions.txt")
}

func LoadSubscriptions() ([]NamedSubscription, error) {
	data, err := safefile.ReadFile(subsPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open subscriptions: %w", err)
	}

	var subs []NamedSubscription
	var pendingComment string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			pendingComment = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		name := pendingComment
		if name == "" {
			name = bareLinkName(line)
		}
		if name == "" {
			if u, err := url.Parse(line); err == nil {
				name = u.Host
			} else {
				name = line
			}
		}
		subs = append(subs, NamedSubscription{URL: line, Name: name})
		pendingComment = ""
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan subscriptions: %w", err)
	}
	return subs, nil
}

func RemoveSubscription(index int) error {
	data, err := safefile.ReadFile(subsPath())
	if err != nil {
		return fmt.Errorf("read subscriptions: %w", err)
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	type entry struct {
		comment string
		url     string
	}

	var entries []entry
	var pendingComment string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			pendingComment = line
			continue
		}
		entries = append(entries, entry{comment: pendingComment, url: line})
		pendingComment = ""
	}

	if index < 0 || index >= len(entries) {
		return fmt.Errorf("invalid index %d (max %d)", index, len(entries)-1)
	}

	entries = append(entries[:index], entries[index+1:]...)

	var sb strings.Builder
	for _, e := range entries {
		if e.comment != "" {
			sb.WriteString(e.comment + "\n")
		}
		sb.WriteString(e.url + "\n")
	}

	return writeFileAtomic(subsPath(), []byte(sb.String()))
}

// writeFileAtomic replaces the list through a temp file (safefile.WriteFile),
// so a crash or a full disk leaves the previous contents intact rather than a
// truncated file. This file is the user's only copy of their subscription
// tokens; losing it costs them access. 0600 for the same reason.
func writeFileAtomic(path string, data []byte) error {
	if err := safefile.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write subscriptions: %w", err)
	}
	return nil
}

// NameSubscription writes name as the comment above rawURL, which is where
// LoadSubscriptions reads a subscription's name from. A subscription that
// already carries a comment is left alone: that name was typed by the user (or
// written here earlier) and is theirs to keep. Doing nothing is a success.
func NameSubscription(rawURL, name string) error {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\n", " "))
	if name == "" {
		return nil
	}

	data, err := safefile.ReadFile(subsPath())
	if err != nil {
		return fmt.Errorf("read subscriptions: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	named := false
	out := make([]string, 0, len(lines)+1)
	commented := false // the previous kept line is this one's comment
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !commented && trimmed == rawURL {
			out = append(out, "# "+name)
			named = true
		}
		out = append(out, line)
		commented = strings.HasPrefix(trimmed, "#")
	}
	if !named {
		return nil
	}
	return writeFileAtomic(subsPath(), []byte(strings.Join(out, "\n")+"\n"))
}

// SaveSubscription adds rawURL to the list unless it is already there. The
// list is rewritten whole rather than appended to: an append opens whatever
// sits at the path, a symlink included (G02), and glues the new URL onto a last
// line that lacks its newline.
func SaveSubscription(rawURL string) error {
	subs, err := LoadSubscriptions()
	if err != nil {
		return fmt.Errorf("check duplicates: %w", err)
	}
	for _, s := range subs {
		if s.URL == rawURL {
			return nil
		}
	}

	data, err := safefile.ReadFile(subsPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("save subscription: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, rawURL+"\n"...)
	return writeFileAtomic(subsPath(), data)
}
