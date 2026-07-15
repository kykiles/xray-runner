package subscription

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchOption customizes the subscription request, e.g. to add HWID headers.
type FetchOption func(*http.Request)

// WithHWID attaches the device-identifying headers the panel uses to bind the
// subscription to a device (A-4). The same headers must be sent on refresh.
func WithHWID(hwid, deviceOS, deviceModel string) FetchOption {
	return func(req *http.Request) {
		req.Header.Set("x-hwid", hwid)
		req.Header.Set("x-device-os", deviceOS)
		req.Header.Set("x-device-model", deviceModel)
	}
}

// defaultUserAgent identifies us as a known subscription client. Some panels
// return an empty body (or a stub) unless the request carries a recognized
// client User-Agent, so we send one by default; callers may override via a
// FetchOption.
const defaultUserAgent = "v2rayNG/1.8.5"

func Fetch(rawURL string, opts ...FetchOption) ([]SubEntry, error) {
	body, err := fetchBody(rawURL, opts...)
	if err != nil {
		return nil, err
	}
	return parse(body)
}

// fetchBody performs the HTTP GET; parsing is left to the caller so that both
// the flat and the profile-aware paths share one request.
func fetchBody(rawURL string, opts ...FetchOption) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("subscription request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	for _, opt := range opts {
		opt(req)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
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
	return body, nil
}

// FetchWithHWID is a thin wrapper kept for existing callers.
func FetchWithHWID(rawURL, hwid, deviceOS, deviceModel string) ([]SubEntry, error) {
	return Fetch(rawURL, WithHWID(hwid, deviceOS, deviceModel))
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
	return b64DecodeAnyPadding(strings.TrimSpace(string(raw)))
}
