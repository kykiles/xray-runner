package neterr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

// The two halves of a subscription URL that must never be printed: the token in
// the path and the one in the query.
const (
	pathToken  = "SECRETTOKEN"
	queryToken = "VERY_SECRET"
)

// timeoutError is a cause that reports a timeout and prints the whole URL, the
// way net's own errors do.
type timeoutError struct{}

func (timeoutError) Error() string {
	return "dial https://panel.example/" + pathToken + ": i/o timeout"
}
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }

func assertHidesTokens(t *testing.T, what, s string) {
	t.Helper()
	for _, token := range []string{pathToken, queryToken} {
		if strings.Contains(s, token) {
			t.Errorf("%s shows %s: %s", what, token, s)
		}
	}
}

func TestSafe_TextNamesOriginAndCauseOnly(t *testing.T) {
	// The Location net/http could not parse: the failure happens inside the
	// library, before any redirect policy of ours runs, and its text quotes the
	// target in full.
	badLocation := fmt.Errorf(
		"failed to parse Location header %q: parse %q: invalid URL escape %q",
		"https://panel.example/"+pathToken+"/%zz?secret="+queryToken,
		"https://panel.example/"+pathToken+"/%zz?secret="+queryToken, "%zz")

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"token in path and query",
			&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken + "?x=" + queryToken, Err: errors.New("connection reset by peer")},
			"Get https://panel.example: сетевая ошибка"},
		{"userinfo",
			&url.Error{Op: "Get", URL: "https://" + pathToken + ":" + queryToken + "@panel.example/sub", Err: errors.New("connection reset by peer")},
			"Get https://panel.example: сетевая ошибка"},
		{"malformed url",
			&url.Error{Op: "parse", URL: "https://panel.example/" + pathToken + "/%zz?x=" + queryToken, Err: url.EscapeError("%zz")},
			"parse " + hidden + ": сетевая ошибка"},
		{"malformed redirect target",
			&url.Error{Op: "Get", URL: "https://panel.example/start", Err: badLocation},
			"Get https://panel.example: сетевая ошибка"},
		{"canceled",
			&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken, Err: context.Canceled},
			"Get https://panel.example: запрос отменён"},
		{"deadline",
			&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken, Err: context.DeadlineExceeded},
			"Get https://panel.example: превышено время ожидания"},
		{"timeout",
			&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken, Err: timeoutError{}},
			"Get https://panel.example: превышено время ожидания"},
		{"host not found",
			&url.Error{Op: "Get", URL: "https://panel.invalid/sub/" + pathToken, Err: &net.DNSError{Err: "no such host", Name: "panel.invalid", IsNotFound: true}},
			"Get https://panel.invalid: узел не найден"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Safe(c.err).Error()
			if got != c.want {
				t.Errorf("Error() = %q, want %q", got, c.want)
			}
			assertHidesTokens(t, "error", got)
		})
	}
}

// Our own refusals are built from a scheme and a host, so there is nothing in
// them to hide and nothing to rewrite.
func TestSafe_LeavesOurOwnErrorAlone(t *testing.T) {
	ours := errors.New("http://panel.example отклонён — обновления скачиваются только по https")
	if got := Safe(ours); got != ours {
		t.Errorf("Safe rewrote an error that is not a *url.Error: %v", got)
	}
}

// The cause is kept for errors.Is and errors.As: callers tell a cancelled fetch
// from a slow one and from a broken one.
func TestSafe_KeepsTheCauseRecognizable(t *testing.T) {
	for _, target := range []error{context.Canceled, context.DeadlineExceeded} {
		err := Safe(&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken, Err: target})
		if !errors.Is(err, target) {
			t.Errorf("errors.Is(err, %v) = false for %v", target, err)
		}
	}

	err := Safe(&url.Error{Op: "Get", URL: "https://panel.example/sub/" + pathToken, Err: timeoutError{}})
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("errors.As lost the timeout for %v", err)
	}
	var withTimeout interface{ Timeout() bool }
	if !errors.As(err, &withTimeout) || !withTimeout.Timeout() {
		t.Errorf("Timeout() = false for %v", err)
	}
}
