package flywheel

import (
	"testing"
	"time"
)

// TestParseResetTime covers the reset-clause forms a limit message carries,
// the roll-over to tomorrow, and garbage (issue #380).
func TestParseResetTime(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, la) // 08:00 LA, 16:00 London
	cases := []struct {
		text string
		want time.Time
	}{
		{"10:20am (America/Los_Angeles)", time.Date(2026, 9, 23, 10, 20, 0, 0, la)},
		{"10am (Europe/London)", time.Date(2026, 9, 24, 10, 0, 0, 0, london)}, // passed today in London
		{"3:05pm", time.Date(2026, 9, 23, 15, 5, 0, 0, la)},
		{"7am", time.Date(2026, 9, 24, 7, 0, 0, 0, la)}, // already past: tomorrow
		{"8am", time.Date(2026, 9, 23, 8, 0, 0, 0, la)}, // exactly now: at or after
		{"12am", time.Date(2026, 9, 24, 0, 0, 0, 0, la)},
		{"12:30PM (America/Los_Angeles)", time.Date(2026, 9, 23, 12, 30, 0, 0, la)},
	}
	for _, tc := range cases {
		got, ok := parseResetTime(tc.text, now)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("parseResetTime(%q) = %v, %v, want %v", tc.text, got, ok, tc.want)
		}
	}
	for _, bad := range []string{"", "soon", "13pm", "10:75am", "10am (Mars/Olympus)", "resets 10am", "25"} {
		if got, ok := parseResetTime(bad, now); ok {
			t.Errorf("parseResetTime(%q) = %v, true, want false", bad, got)
		}
	}
}
