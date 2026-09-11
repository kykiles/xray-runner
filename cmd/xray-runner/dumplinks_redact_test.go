package main

import (
	"strings"
	"testing"
)

// A04: --dump-links prints its errors to stderr, which ends up in terminal
// scrollback and CI logs; the subscription token must not ride along.
func TestKeysFilePath_ErrorsHideToken(t *testing.T) {
	for _, raw := range []string{
		"https:///sub/SECRETTOKEN?x=SECRET2",                  // no host
		"https://panel.example/sub/SECRETTOKEN\x7f?x=SECRET2", // unparseable
	} {
		_, err := keysFilePath(raw)
		if err == nil {
			t.Fatalf("keysFilePath(%q) succeeded", raw)
		}
		if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "SECRET2") {
			t.Errorf("error leaks the token: %v", err)
		}
	}
}
