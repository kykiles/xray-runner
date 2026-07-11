### Task 2: Create `internal/subscription/store.go` + tests

**Files:**
- Create: `internal/subscription/store.go`
- Create: `internal/subscription/store_test.go`

**Interfaces:**
- Produces: `NamedSubscription{URL, Name}`, `LoadSubscriptions() ([]NamedSubscription, error)`, `SaveSubscription(rawURL string) error`

- [ ] **Step 1: Write the failing test**

```go
// internal/subscription/store_test.go
package subscription

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSubscriptions_FileNotExist(t *testing.T) {
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(t.TempDir(), "nonexistent.txt")
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions on missing file: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected empty slice, got %d items", len(subs))
	}
}

func TestSaveAndLoadSubscription(t *testing.T) {
	dir := t.TempDir()
	orig := subscriptionsFile
	subscriptionsFile = filepath.Join(dir, "subscriptions.txt")
	defer func() { subscriptionsFile = orig }()

	url := "https://example.com/sub"
	if err := SaveSubscription(url); err != nil {
		t.Fatalf("SaveSubscription: %v", err)
	}

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}
	if subs[0].URL != url {
		t.Errorf("URL = %q, want %q", subs[0].URL, url)
	}
	if subs[0].Name == "" {
		t.Error("expected non-empty Name")
	}
}

func TestLoadSubscriptions_WithComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "# My VPN\nhttps://vpn.example.com/sub\n\n# Backup\nhttps://backup.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "My VPN" {
		t.Errorf("Name[0] = %q, want %q", subs[0].Name, "My VPN")
	}
	if subs[1].Name != "Backup" {
		t.Errorf("Name[1] = %q, want %q", subs[1].Name, "Backup")
	}
}

func TestLoadSubscriptions_WithoutComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.txt")
	content := "https://vpn.example.com/sub\nhttps://other.example.com/sub\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	orig := subscriptionsFile
	subscriptionsFile = path
	defer func() { subscriptionsFile = orig }()

	subs, err := LoadSubscriptions()
	if err != nil {
		t.Fatalf("LoadSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(subs))
	}
	if subs[0].Name != "vpn.example.com" {
		t.Errorf("Name[0] = %q, want hostname", subs[0].Name)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -run TestLoad -v
```
Expected: FAIL (package not found or undefined).

- [ ] **Step 3: Write minimal implementation**

```go
// internal/subscription/store.go
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

func SaveSubscription(rawURL string) error {
	f, err := os.OpenFile(subscriptionsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("save subscription: %w", err)
	}
	defer f.Close()

	// Check duplicate
	subs, _ := LoadSubscriptions()
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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -run TestLoad -v
```
Expected: all PASS.

- [ ] **Step 5: Run all existing tests to ensure no regression**

```bash
cd D:\user\efimov_p\USB-Flash\projects\Xray
go test ./internal/subscription/ -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/subscription/store.go internal/subscription/store_test.go
git commit -m "feat: add subscription persistence (store.go)"
```
