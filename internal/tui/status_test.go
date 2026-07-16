package tui

import (
	"testing"
	"time"
)

func TestFmtDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00"},
		{90 * time.Second, "00:01:30"},
		{25*time.Hour + 30*time.Minute, "1d 01:30:00"},
		{49 * time.Hour, "2d 01:00:00"},
	}
	for _, tt := range tests {
		if got := fmtDuration(tt.d); got != tt.want {
			t.Errorf("fmtDuration(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
