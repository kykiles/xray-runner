package subscription

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"time"
)

func Fetch(rawURL string) ([]SubEntry, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("subscription fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription fetch: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("subscription read: %w", err)
	}

	return parse(body)
}

func parse(raw []byte) ([]SubEntry, error) {
	decoded, decodedErr := tryBase64Decode(raw)
	if decodedErr == nil {
		entries, err := tryParseJSON(decoded)
		if err == nil {
			return entries, nil
		}
		return parseURLList(string(decoded))
	}

	entries, err := tryParseJSON(raw)
	if err == nil {
		return entries, nil
	}

	return parseURLList(string(raw))
}

func tryBase64Decode(raw []byte) ([]byte, error) {
	s := string(raw)
	s = trimSpace(s)

	// base64 decode with padding fix
	if m := len(s) % 4; m != 0 {
		s += string("===="[:4-m])
	}

	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// try without padding
		decoded, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			// try URL-safe encoding (common in V2Ray)
			if m2 := len(s) % 4; m2 != 0 {
				s += string("===="[:4-m2])
			}
			decoded, err = base64.URLEncoding.DecodeString(s)
			if err != nil {
				decoded, err = base64.RawURLEncoding.DecodeString(s)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func trimSpace(s string) string {
	// Remove leading/trailing whitespace
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	if start > 0 || end < len(s) {
		return s[start:end]
	}
	return s
}
