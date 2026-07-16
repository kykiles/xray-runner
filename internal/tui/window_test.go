package tui

import "testing"

// window keeps the cursor inside a height-row slice and reports the hidden
// counts above/below, so the scroll indicators are correct (task #3).
func TestWindow(t *testing.T) {
	tests := []struct {
		name                             string
		total, cursor, height            int
		wantStart, wantEnd, wantA, wantB int
	}{
		{"fits", 5, 2, 10, 0, 5, 0, 0},
		{"unknown height shows all", 100, 50, 0, 0, 100, 0, 0},
		{"top", 100, 0, 10, 0, 10, 0, 90},
		{"middle centers", 100, 50, 10, 45, 55, 45, 45},
		{"bottom clamps", 100, 99, 10, 90, 100, 90, 0},
		{"near top no negative", 100, 3, 10, 0, 10, 0, 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, above, below := window(tt.total, tt.cursor, tt.height)
			if start != tt.wantStart || end != tt.wantEnd || above != tt.wantA || below != tt.wantB {
				t.Errorf("window(%d,%d,%d) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					tt.total, tt.cursor, tt.height, start, end, above, below,
					tt.wantStart, tt.wantEnd, tt.wantA, tt.wantB)
			}
			// The cursor must always land inside the rendered window.
			if tt.height > 0 && tt.total > tt.height {
				if tt.cursor < start || tt.cursor >= end {
					t.Errorf("cursor %d outside window [%d,%d)", tt.cursor, start, end)
				}
			}
		})
	}
}
