package xraycfg

import "testing"

func TestHasBypassRules(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "everything through the proxy",
			raw:  `{"routing":{"rules":[{"outboundTag":"proxy","domain":["geosite:geolocation-!ru"]}]}}`,
			want: false,
		},
		{
			name: "panel profile sends .ru direct",
			raw:  `{"routing":{"rules":[{"outboundTag":"proxy"},{"outboundTag":"direct","domain":["geosite:category-ru"]}]}}`,
			want: true,
		},
		{
			name: "block counts too",
			raw:  `{"routing":{"rules":[{"outboundTag":"block","domain":["geosite:category-ads"]}]}}`,
			want: true,
		},
		{
			name: "blocked spelling",
			raw:  `{"routing":{"rules":[{"outboundTag":"blocked"}]}}`,
			want: true,
		},
		{
			// A balancer groups proxy outbounds, so a rule aimed at one is not a
			// bypass — whatever the balancer happens to be called.
			name: "balancer is not a bypass",
			raw:  `{"routing":{"rules":[{"balancerTag":"direct"}]}}`,
			want: false,
		},
		{
			name: "no routing section",
			raw:  `{"inbounds":[]}`,
			want: false,
		},
		{
			name: "garbage is not a bypass claim",
			raw:  `not json at all`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasBypassRules([]byte(tt.raw)); got != tt.want {
				t.Errorf("HasBypassRules() = %v, want %v", got, tt.want)
			}
		})
	}
}
