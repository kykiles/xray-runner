package subscription

import (
	"net/url"
	"strings"

	"xray-runner/internal/neterr"
)

// RedactURL cuts a subscription URL down to scheme://host/…. The token lives in
// the path or query, so nothing past the host may reach a log line, an error or
// the screen (A04). A string that does not parse as a URL is hidden whole.
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return "<ссылка скрыта>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// RedactURLError replaces a network error whose text is the full URL, token
// included — and, when a redirect target could not be parsed, the target's URL
// as well — with one naming only the origin, the operation and a safe cause.
// The original stays reachable through Unwrap, so errors.Is for a timeout or a
// cancellation still works. Any error that is not a *url.Error comes back
// unchanged.
func RedactURLError(err error) error { return neterr.Safe(err) }
