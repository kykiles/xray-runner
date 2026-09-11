package subscription

import (
	"errors"
	"net/url"
	"strings"
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

// RedactURLError rewrites the URL inside a *url.Error — whose text is the full
// URL, token included — keeping Op and Err, so errors.Is for a timeout or a
// cancellation still works. Meant for the error straight from net/http or
// net/url, before any wrapping; any other error comes back unchanged.
func RedactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: RedactURL(ue.URL), Err: ue.Err}
}
