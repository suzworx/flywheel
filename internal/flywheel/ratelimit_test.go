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

// TestRateLimitPaused covers the pause a rate-limited finished event's
// reset_at puts on its model (issue #383).
func TestRateLimitPaused(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	reset := now.Add(20 * time.Minute)
	limited := func(model string, at time.Time) Event {
		return Event{Kind: "finished", Task: "T1", Model: model, Reason: "rate-limited", ResetAt: at.Format(time.RFC3339)}
	}
	cases := []struct {
		name   string
		events []Event
		model  string
		paused bool
	}{
		{"no events", nil, "m", false},
		{"pending reset", []Event{limited("m", reset)}, "m", true},
		{"expired reset", []Event{limited("m", now.Add(-time.Minute))}, "m", false},
		{"clean stop after the limit", []Event{limited("m", reset), {Kind: "finished", Task: "T2", Model: "m", Reason: "stop"}}, "m", false},
		{"error after the limit", []Event{limited("m", reset), {Kind: "finished", Task: "T2", Model: "m", Reason: "error"}}, "m", true},
		{"a different model", []Event{limited("other", reset)}, "m", false},
	}
	for _, c := range cases {
		until, paused := rateLimitPaused(c.events, c.model, now)
		if paused != c.paused {
			t.Errorf("%s: paused = %v, want %v", c.name, paused, c.paused)
		}
		if paused && !until.Equal(reset) {
			t.Errorf("%s: until = %v, want %v", c.name, until, reset)
		}
	}
	models, at := pausedModels([]Event{limited("a", reset), limited("b", now.Add(-time.Minute)), limited("a", reset)}, now)
	if len(models) != 1 || models[0] != "a" || !at["a"].Equal(reset) {
		t.Errorf("pausedModels = %v %v, want [a] at %v", models, at, reset)
	}
}
