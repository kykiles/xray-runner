package app

import (
	"strings"
	"testing"
)

// A04: a subscription URL with a typo in the scheme is still the user's token.
// The refusal names the host so the typo is findable, and nothing past it.
func TestValidateSubscriptionInput_RefusalHidesToken(t *testing.T) {
	err := validateSubscriptionInput("htps://panel.example/sub/SECRETTOKEN?x=SECRET2")
	if err == nil {
		t.Fatal("a non-http(s) URL was accepted")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "SECRET2") {
		t.Errorf("refusal leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "htps://panel.example") {
		t.Errorf("refusal %q lost the scheme and host", err)
	}
}
