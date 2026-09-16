// Package neterr turns a failed HTTP request into an error that is safe to log
// and to show. net/http prints the whole request URL — a subscription token
// lives in its path or its query — and the cause it wraps may print another URL
// on its own: a Location the library could not parse is quoted in full, before
// any redirect policy of ours gets to see it. Cutting one more URL field is not
// enough, so the text is rebuilt here instead of edited (F06).
package neterr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// hidden stands in for an origin that could not be read. The raw string is the
// thing being hidden, so it is never shown in its place.
const hidden = "<ссылка скрыта>"

// safeError describes a network failure by its origin, its operation and one of
// a fixed set of causes. The original error stays reachable through Unwrap for
// errors.Is and errors.As — never for its text.
type safeError struct {
	op     string
	origin string
	cause  string
	err    error
}

func (e *safeError) Error() string {
	if e.op == "" {
		return fmt.Sprintf("%s: %s", e.origin, e.cause)
	}
	return fmt.Sprintf("%s %s: %s", e.op, e.origin, e.cause)
}

// Unwrap keeps the original for errors.Is and errors.As. Callers ask it what
// kind of failure this was; they do not print what it returns.
func (e *safeError) Unwrap() error { return e.err }

// Timeout answers the net.Error question through the original instead of
// through its text.
func (e *safeError) Timeout() bool {
	var ne net.Error
	return errors.As(e.err, &ne) && ne.Timeout()
}

// Safe replaces what net/http and net/url return with an error naming only the
// origin, the operation and a safe cause. An error that is not a *url.Error was
// built by us — from a scheme and a host — and comes back unchanged.
func Safe(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &safeError{op: ue.Op, origin: origin(ue.URL), cause: cause(err), err: err}
}

// origin cuts a URL down to scheme://host. The token lives in the path or the
// query, and the userinfo is in neither, so what is left names the party that
// was talked to and nothing else. A URL that does not parse is hidden whole —
// it is the malformed one whose text the error was printing.
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return hidden
	}
	return u.Scheme + "://" + u.Host
}

// cause names what went wrong in words of our own: the text of the original
// cause is exactly what may hold a URL. Anything not recognized here is a
// network failure and nothing more.
func cause(err error) string {
	var ne net.Error
	var dns *net.DNSError
	switch {
	case errors.Is(err, context.Canceled):
		return "запрос отменён"
	case errors.Is(err, context.DeadlineExceeded):
		return "превышено время ожидания"
	case errors.As(err, &ne) && ne.Timeout():
		return "превышено время ожидания"
	case errors.As(err, &dns) && dns.IsNotFound:
		return "узел не найден"
	}
	return "сетевая ошибка"
}
