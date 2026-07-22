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
		{
			name: "single-server profile returns to the profile screen",
			tgt: target{
				entry:        &subscription.SubEntry{Address: "example.com", Port: 443},
				fromProfiles: true,
			},
			want: levelProfiles,
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

// A panel commonly publishes an autoselect profile alongside per-location ones,
// so the same server appears in several profiles under different outbound tags.
// The server screen was opened from a known profile, and that profile — not the
// first one holding a matching endpoint — is the one whose routing must run:
// pinning the tag against the wrong profile silently connects elsewhere.
func TestOwningProfile_UsesTheOpenedProfileNotFirstMatch(t *testing.T) {
	serverB := subscription.SubEntry{
		Protocol: "vless", Address: "b.example.com", Port: 443,
		UUID: "11111111-2222-3333-4444-555555555555",
	}
	autoselect := subscription.Profile{
		Name: "Автовыбор",
		Raw:  []byte(`{"remarks":"Автовыбор"}`),
		Entries: []subscription.SubEntry{
			{Protocol: "vless", Address: "a.example.com", Port: 443, UUID: "aaaaaaaa-2222-3333-4444-555555555555"},
			serverB,
		},
		Balancer: &subscription.BalancerInfo{Tag: "balancer", Strategy: "random"},
	}
	germany := subscription.Profile{
		Name:    "Germany",
		Raw:     []byte(`{"remarks":"Germany"}`),
		Entries: []subscription.SubEntry{serverB},
	}
	profiles := []subscription.Profile{autoselect, germany}

	t.Run("expanded profile wins over first match", func(t *testing.T) {
		n := &nav{profiles: profiles, profIdx: 1}
		got := n.owningProfile(&serverB)
		if got == nil {
			t.Fatal("owningProfile returned nil")
		}
		if got.Name != "Germany" {
			t.Errorf("owningProfile = %q, want %q", got.Name, "Germany")
		}
	})

	t.Run("flat list falls back to endpoint match", func(t *testing.T) {
		n := &nav{profiles: profiles, flat: true}
		got := n.owningProfile(&serverB)
		if got == nil {
			t.Fatal("owningProfile returned nil")
		}
		if got.Name != "Автовыбор" {
			t.Errorf("owningProfile = %q, want %q", got.Name, "Автовыбор")
		}
	})
}
