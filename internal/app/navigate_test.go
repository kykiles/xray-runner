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

// A single server running under its profile's routing may be sent to a sibling
// outbound by the panel's rules, so every server of the profile has to stay on
// the physical path.
func TestServerHosts_SingleServerCoversItsProfile(t *testing.T) {
	tgt := target{
		entry: &subscription.SubEntry{Address: "b.invalid"},
		profileSrvs: []subscription.SubEntry{
			{Address: "a.invalid"},
			{Address: "b.invalid"},
		},
	}
	hosts := tgt.serverHosts()
	if len(hosts) != 2 {
		t.Fatalf("hosts = %v, want both servers exactly once", hosts)
	}
	if hosts[0] != "b.invalid" || hosts[1] != "a.invalid" {
		t.Errorf("hosts = %v, want the chosen server first", hosts)
	}
}

// A bare link has no profile behind it: one host, as before.
func TestServerHosts_BareEntry(t *testing.T) {
	tgt := target{entry: &subscription.SubEntry{Address: "a.invalid"}}
	if hosts := tgt.serverHosts(); len(hosts) != 1 || hosts[0] != "a.invalid" {
		t.Errorf("hosts = %v, want [a.invalid]", hosts)
	}
}
