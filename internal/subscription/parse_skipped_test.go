package subscription

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// captureLogs redirects slog at Debug level for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A subscription where most servers fail to parse currently yields a short list
// with no explanation anywhere — not even at Debug level.
func TestParseURLList_LogsSkippedLines(t *testing.T) {
	buf := captureLogs(t)

	data := strings.Join([]string{
		"vless://b831381d-6324-4d53-ad4f-8cda48b30811@a.example.com:443?type=tcp#ok",
		"ftp://unsupported.example.com",
		"vmess://not-base64!!!",
	}, "\n")

	entries, err := parseURLList(data)
	if err != nil {
		t.Fatalf("parseURLList: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("nothing logged about the 2 skipped lines: %q", buf.String())
	}
}

func TestParseJSONArray_LogsSkippedEntries(t *testing.T) {
	buf := captureLogs(t)

	entries, err := parseJSONArray(rawMessages(t,
		`{"protocol":"vless","address":"a.example.com","port":443,"uuid":"b831381d-6324-4d53-ad4f-8cda48b30811"}`,
		`{"nothing":"useful"}`,
	))
	if err != nil {
		t.Fatalf("parseJSONArray: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("nothing logged about the skipped entry: %q", buf.String())
	}
}

func rawMessages(t *testing.T, items ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(items))
	for _, s := range items {
		out = append(out, json.RawMessage(s))
	}
	return out
}
