package subscription

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxSubscriptionBody caps how much of a subscription response we read, so a
// broken or hostile server can't exhaust memory. Real subscriptions are a few KB.
const maxSubscriptionBody = 10 << 20

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
// Panels template their response per client User-Agent and answer with 502/404
// for a client they have no template for. Happ comes first because it yields
// the Xray-config profiles this app is built around; v2rayNG is the fallback
// for panels that only know the base64 link list.
var userAgents = []string{"Happ/1.0", "v2rayNG/1.8.5"}

// hwidHeaders are the device-identifying headers WithHWID sets. Go strips only
// Authorization/Cookie/WWW-Authenticate across hosts, so these need dropping by
// hand or a redirect hands the user's device id to a third party.
var hwidHeaders = []string{"x-hwid", "x-device-os", "x-device-model"}

// checkRedirect keeps a subscription request from downgrading to plaintext and
// from carrying the HWID headers off the original host. Panels redirect
// legitimately, so redirects themselves stay allowed.
func checkRedirect(req *http.Request, via []*http.Request) error {
	prev := via[len(via)-1]
	if prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("подписка перенаправлена с https на %s://%s — отказ, токен и заголовки устройства ушли бы открытым текстом",
			req.URL.Scheme, req.URL.Host)
	}
	if req.URL.Host != prev.URL.Host {
		for _, h := range hwidHeaders {
			req.Header.Del(h)
		}
	}
	return nil
}

func Fetch(rawURL string, opts ...FetchOption) ([]SubEntry, error) {
	rawURL, err := unwrapHapp(rawURL)
	if err != nil {
		return nil, err
	}

	// A bare link carries the server inline — there is nothing to fetch, and the
	// branch lives here so every caller (menu, scripted selection, --dump-links)
	// gets it without repeating the check.
	if IsBareLink(rawURL) {
		e, err := ParseBareLink(rawURL)
		if err != nil {
			return nil, err
		}
		return []SubEntry{*e}, nil
	}

	body, err := fetchBody(rawURL, opts...)
	if err != nil {
		return nil, err
	}
	return parse(body)
}

// fetchBody performs the HTTP GET; parsing is left to the caller so that both
// the flat and the profile-aware paths share one request.
func fetchBody(rawURL string, opts ...FetchOption) ([]byte, error) {
	// A plain-http subscription sends the token and the x-hwid headers in the
	// clear. We still allow it (self-hosted panels exist) but warn loudly.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "http://") {
		slog.Warn("подписка запрашивается по незашифрованному http:// — токен и заголовки устройства идут открытым текстом", "url", rawURL)
	}

	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: checkRedirect}

	var resp *http.Response
	for i, ua := range userAgents {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("subscription request: %w", err)
		}
		req.Header.Set("User-Agent", ua)
		for _, opt := range opts {
			opt(req)
		}

		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("subscription fetch: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
		resp.Body.Close()
		if i == len(userAgents)-1 {
			return nil, fmt.Errorf("subscription fetch: HTTP %d", resp.StatusCode)
		}
		slog.Debug("подписка не ответила на User-Agent, пробуем следующий", "ua", ua, "status", resp.StatusCode)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBody+1))
	if err != nil {
		return nil, fmt.Errorf("subscription read: %w", err)
	}
	if len(body) > maxSubscriptionBody {
		return nil, fmt.Errorf("subscription too large: превышает лимит %d байт", maxSubscriptionBody)
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
