package subscription

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
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

	return os.WriteFile(subscriptionsFile, []byte(sb.String()), 0600)
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
