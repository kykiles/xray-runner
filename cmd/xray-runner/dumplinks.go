package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
)

// dumpLinks fetches the subscription, reconstructs bare links, and writes them
// to keys/<DOMAIN>.md. Returns the file path on success.
func dumpLinks(cfg *config.Config, subURL string) (string, error) {
	path, err := keysFilePath(subURL)
	if err != nil {
		return "", err
	}

	hwid := config.GetOrCreateHWID(cfg.HWID)
	entries, err := subscription.FetchWithHWID(subURL, hwid, runtime.GOOS, cfg.HWIDDeviceModel)
	if err != nil {
		return "", fmt.Errorf("fetch subscription: %w", err)
	}

	md := renderMarkdown(subURL, entries, time.Now())

	if err := os.MkdirAll("keys", 0o750); err != nil {
		return "", fmt.Errorf("create keys dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// keysFilePath maps a subscription URL to keys/<HOST>.md, host uppercased,
// port and path stripped.
func keysFilePath(subURL string) (string, error) {
	u, err := url.Parse(subURL)
	if err != nil {
		return "", fmt.Errorf("parse subscription url: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("subscription url has no host: %q", subURL)
	}
	return filepath.Join("keys", strings.ToUpper(host)+".md"), nil
}

// protocolOrder fixes section ordering so output is stable across runs.
var protocolOrder = []string{"vless", "vmess", "ss", "hysteria2"}

func protocolLabel(proto string) string {
	if proto == "hysteria" {
		return "HYSTERIA2"
	}
	return strings.ToUpper(proto)
}

// renderMarkdown groups entries by protocol into sections; each item is labeled
// with its remarks (falling back to the bare link when remarks is empty).
func renderMarkdown(subURL string, entries []subscription.SubEntry, now time.Time) string {
	byProto := map[string][]subscription.SubEntry{}
	for _, e := range entries {
		p := e.Protocol
		if p == "hysteria" {
			p = "hysteria2"
		}
		byProto[p] = append(byProto[p], e)
	}

	// Known protocols first (fixed order), then any others alphabetically.
	var protos []string
	for _, p := range protocolOrder {
		if len(byProto[p]) > 0 {
			protos = append(protos, p)
		}
	}
	var extra []string
	for p := range byProto {
		known := false
		for _, k := range protocolOrder {
			if k == p {
				known = true
				break
			}
		}
		if !known {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	protos = append(protos, extra...)

	var b strings.Builder
	host := subURL
	if u, err := url.Parse(subURL); err == nil && u.Hostname() != "" {
		host = strings.ToUpper(u.Hostname())
	}
	fmt.Fprintf(&b, "# %s\n\n", host)
	fmt.Fprintf(&b, "_Источник: %s · %s_\n", subURL, now.Format("02.01.2006"))

	for _, p := range protos {
		fmt.Fprintf(&b, "\n## %s\n\n", protocolLabel(p))
		for i, e := range byProto[p] {
			link, err := subscription.EncodeURL(&e)
			if err != nil {
				fmt.Fprintf(&b, "%d. ⚠️ %v\n\n", i+1, err)
				continue
			}
			if e.Remarks != "" {
				fmt.Fprintf(&b, "%d. %s\n   `%s`\n\n", i+1, e.Remarks, link)
			} else {
				fmt.Fprintf(&b, "%d. `%s`\n\n", i+1, link)
			}
		}
	}
	return b.String()
}
