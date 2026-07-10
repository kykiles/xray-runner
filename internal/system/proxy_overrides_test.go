//go:build windows

package system

import "testing"

func TestFormatOverrides(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "<-loopback>"},
		{"already present", "<-loopback>", "<-loopback>"},
		{"present with others", "<-loopback>;*.local", "<-loopback>;*.local"},
		{"present trailing", "*.local;<-loopback>", "*.local;<-loopback>"},
		{"missing appended", "*.local", "*.local;<-loopback>"},
		{"missing multiple", "*.local;192.168.0.0/16", "*.local;192.168.0.0/16;<-loopback>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatOverrides(c.in); got != c.want {
				t.Errorf("formatOverrides(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
