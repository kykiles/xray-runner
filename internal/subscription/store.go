package subscription

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type NamedSubscription struct {
	URL  string
	Name string
}

var subscriptionsFile = "subscriptions.txt"

func LoadSubscriptions() ([]NamedSubscription, error) {
	f, err := os.Open(subscriptionsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open subscriptions: %w", err)
	}
	defer f.Close()

	var subs []NamedSubscription
	var pendingComment string
	scanner := bufio.NewScanner(f)
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
	data, err := os.ReadFile(subscriptionsFile)
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

	return writeFileAtomic(subscriptionsFile, []byte(sb.String()))
}

// writeFileAtomic replaces a file through a temp file in the same directory, so
// a crash or a full disk leaves the previous contents intact rather than a
// truncated one. This file is the user's only copy of their subscription
// tokens; losing it costs them access.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".subscriptions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp subscriptions: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()        // already closed on the success path
		_ = os.Remove(tmpName) // no-op once the rename succeeded
	}()

	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("chmod temp subscriptions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp subscriptions: %w", err)
	}
	// Without the sync the rename can land before the bytes do, leaving an empty
	// file after a power loss.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp subscriptions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp subscriptions: %w", err)
	}
	return os.Rename(tmpName, path)
}

func SaveSubscription(rawURL string) error {
	f, err := os.OpenFile(subscriptionsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("save subscription: %w", err)
	}
	defer f.Close()

	subs, err := LoadSubscriptions()
	if err != nil {
		return fmt.Errorf("check duplicates: %w", err)
	}
	for _, s := range subs {
		if s.URL == rawURL {
			return nil
		}
	}

	if _, err := fmt.Fprintln(f, rawURL); err != nil {
		return fmt.Errorf("write subscription: %w", err)
	}
	return nil
}
