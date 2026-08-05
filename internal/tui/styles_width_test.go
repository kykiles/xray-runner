package tui

import "testing"

// The three placements the panel names actually use: an emoji opening the name,
// one sitting inside it, one trailing — plus the runs of several in a row that
// made the old width machinery drift columns left.
func TestStripEmoji(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"leading flag", "🇩🇪 Germany Munich 01", "Germany Munich 01"},
		{"leading run", "🇩🇪🇳🇱⚡ Europe Pool", "Europe Pool"},
		{"leading with separator", "🇩🇪 | DE-01", "DE-01"},
		{"inside collapses to one space", "Germany☁️Munich", "Germany Munich"},
		{"inside run collapses to one space", "Germany 🟢🟢 Munich", "Germany Munich"},
		{"trailing", "Amsterdam 🇳🇱", "Amsterdam"},
		{"trailing run", "Amsterdam ⚡⚡", "Amsterdam"},
		{"cyrillic survives", "🤝 Подписка Ялта", "Подписка Ялта"},
		{"digits open a name", "🇩🇪 01 Munich", "01 Munich"},
		{"no emoji is untouched", "Frankfurt DE-02", "Frankfurt DE-02"},
		{"placeholder keeps its bracket", "(без имени)", "(без имени)"},
		{"emoji only", "🇩🇪⚡", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripEmoji(c.in); got != c.want {
				t.Errorf("stripEmoji(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// What the whole point was: two names differing only by an emoji must pad to the
// same column, so the table's NAME column stops drifting.
func TestStripEmoji_PadsAlike(t *testing.T) {
	a := pad(stripEmoji("☁️Frankfurt"), 20)
	b := pad(stripEmoji("Frankfurt"), 20)
	if a != b {
		t.Errorf("padded names differ: %q vs %q", a, b)
	}
}
