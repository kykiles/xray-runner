package tui

import "testing"

// A VS16 emoji whose base is narrow on its own (☁️) advances the terminal by one
// column, not the two lipgloss counts — the profile names that carry it used to
// drag every column after them a cell to the left.
func TestDispWidth_NarrowVS16(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"☁️", 1},
		{"⚪️", 2}, // base is already wide — untouched
		{"🇩🇪", 2},
		{"AA", 2},
		{"🇩🇪 ⚪️🟢 ☁️FRDM", 13},
	}
	for _, c := range cases {
		if got := dispWidth(c.s); got != c.want {
			t.Errorf("dispWidth(%q) = %d, want %d", c.s, got, c.want)
		}
	}
	// Padding is what the table relies on: two names differing only by the cloud
	// must still end at the same column.
	if a, b := pad("☁️x", 6), pad("x", 6); dispWidth(a) != dispWidth(b) {
		t.Errorf("padded widths differ: %d vs %d", dispWidth(a), dispWidth(b))
	}
}
