package subscription

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"xray-runner/internal/config"
	"xray-runner/internal/safefile"
	"xray-runner/internal/secret"
)

type NamedSubscription struct {
	URL  string
	Name string
}

// subscriptionsFile overrides where the list lives; empty means "resolve it",
// which is what every run does. Tests set it to a temp file, and the sealed
// copy then sits beside it.
var subscriptionsFile = ""

// sealer is where the sealing key comes from. Overridable in tests.
var sealer = secret.Default

const (
	plainName  = "subscriptions.txt"
	sealedName = "subscriptions.enc"
)

// store is where the list lives. The subscription URLs carry the panel's
// access tokens, so the list is sealed with a key the OS keeps for the user
// (package secret): subscriptions.enc in the data dir. Two cases stay plain:
//
//   - portable: a subscriptions.txt with something in it next to the program.
//     A key the OS keeps is bound to this user on this machine, and a list
//     that travels on a flash drive would not open on the next one. Moving
//     the file into the data dir seals it on the next start;
//   - no key store: a headless server without a Secret Service. The file is
//     kept 0600 as before, and StorageNote says so.
//
// A plain list found in the data dir — every install before this — is sealed
// the first time it is read, and the plain copy removed once the sealed one
// is written.
type store struct {
	plain, sealed string
	portable      bool
}

func locate() store {
	if subscriptionsFile != "" {
		return store{plain: subscriptionsFile, sealed: filepath.Join(filepath.Dir(subscriptionsFile), sealedName)}
	}
	if dir := config.ProgramDir(); dir != "" {
		p := filepath.Join(dir, plainName)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return store{plain: p, portable: true}
		}
	}
	dir := config.DataDir()
	if dir == "" {
		// Nowhere of the user's own: next to the program, as before.
		p := config.Path(plainName)
		return store{plain: p, portable: true}
	}
	return store{plain: filepath.Join(dir, plainName), sealed: filepath.Join(dir, sealedName)}
}

// readSubs returns the list as text, from whichever form it is kept in.
func readSubs() ([]byte, error) {
	st := locate()
	if st.portable {
		return safefile.ReadFile(st.plain)
	}
	blob, err := safefile.ReadFile(st.sealed)
	if err == nil {
		s, err := sealer()
		if err != nil {
			return nil, fmt.Errorf("подписки зашифрованы (%s), но ключ недоступен: %w", st.sealed, err)
		}
		data, err := s.Open(blob)
		if err != nil {
			return nil, fmt.Errorf("подписки %s (%s): %w", st.sealed, s.Name(), err)
		}
		return data, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	data, err := safefile.ReadFile(st.plain)
	if err != nil {
		return nil, err
	}
	if _, err := sealer(); err == nil && len(bytes.TrimSpace(data)) > 0 {
		// Best-effort: the list is read either way.
		if err := writeSubs(data); err != nil {
			slog.Warn("subscriptions not sealed", "error", err)
		}
	}
	return data, nil
}

// writeSubs replaces the list, sealed unless the store says otherwise.
func writeSubs(data []byte) error {
	st := locate()
	if st.portable {
		return writeFileAtomic(st.plain, data)
	}
	s, err := sealer()
	if err != nil {
		return writeFileAtomic(st.plain, data)
	}
	blob, err := s.Seal(data)
	if err != nil {
		return fmt.Errorf("write subscriptions: %w", err)
	}
	if err := writeFileAtomic(st.sealed, blob); err != nil {
		return err
	}
	// Only once the sealed copy is on disk; a plain one left behind would still
	// hand the tokens to whoever reads it.
	if err := os.Remove(st.plain); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("зашифрованная копия записана, открытая не удалена: %w", err)
	}
	return nil
}

// StorageNote says how the list is kept when it is not sealed, and why — for
// the screen once per start — and is empty when it is sealed.
func StorageNote() string {
	st := locate()
	if st.portable {
		return ""
	}
	if _, err := sealer(); err != nil {
		return "Подписки хранятся без шифрования, в файле с доступом только для вас: " + err.Error() + "."
	}
	return ""
}

func LoadSubscriptions() ([]NamedSubscription, error) {
	data, err := readSubs()
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
	data, err := readSubs()
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

	return writeSubs([]byte(sb.String()))
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

	data, err := readSubs()
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
	return writeSubs([]byte(strings.Join(out, "\n") + "\n"))
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

	data, err := readSubs()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("save subscription: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, rawURL+"\n"...)
	return writeSubs(data)
}
