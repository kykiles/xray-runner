package updater

import "testing"

func TestSameVersion(t *testing.T) {
	cases := []struct {
		tag, installed string
		want           bool
	}{
		{"v26.6.27", "26.6.27", true},
		{"26.6.27", "26.6.27", true},
		{"V26.6.27", "v26.6.27", true},
		{" v26.6.27 ", "26.6.27", true},
		{"v26.6.27", "26.6.28", false},
		{"v26.6.27", "", false},
		{"", "26.6.27", false},
	}
	for _, c := range cases {
		if got := SameVersion(c.tag, c.installed); got != c.want {
			t.Errorf("SameVersion(%q, %q) = %v, want %v", c.tag, c.installed, got, c.want)
		}
	}
}
