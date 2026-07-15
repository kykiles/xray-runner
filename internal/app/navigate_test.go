package app

import (
	"testing"

	"xray-runner/internal/subscription"
)

// A balancer profile is chosen on the profile screen, so "back" must return
// there — not into the server list the user never opened.
func TestBackLevel(t *testing.T) {
	tests := []struct {
		name string
		tgt  target
		want navLevel
	}{
		{
			name: "profile returns to the profile screen",
			tgt:  target{profileName: "Balanced", profileRaw: []byte(`{}`)},
			want: levelProfiles,
		},
		{
			name: "single server returns to the server list",
			tgt:  target{entry: &subscription.SubEntry{Address: "example.com", Port: 443}},
			want: levelServers,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := backLevel(&tc.tgt); got != tc.want {
				t.Errorf("backLevel() = %v, want %v", got, tc.want)
			}
		})
	}
}
