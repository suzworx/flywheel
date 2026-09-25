package flywheel

import (
	"testing"
	"time"
)

// TestParseResetTime covers the reset-clause forms a limit message carries,
// the roll-over to tomorrow, and garbage (issue #380).
func TestParseResetTime(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	models, at := pausedModels([]Event{limited("a", reset), limited("b", now.Add(-time.Minute)), limited("a", reset)}, now, 0.95)
	if len(models) != 1 || models[0] != "a" || !at["a"].Until.Equal(reset) {
		t.Errorf("pausedModels = %v %v, want [a] at %v", models, at, reset)
	}
}

// TestPauseAtUtilization: a model whose latest finish recorded utilization at
// or above the threshold with a future limit_reset_at is paused until that
// reset, before the limit hits; a later finish below the threshold, an
// expired reset or a disabled threshold releases it; the refusal text and the
// andon line name the cause (issue #417).
func TestPauseAtUtilization(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Minute)
	fin := func(model, reason string, util float64, at time.Time) Event {
		return Event{Kind: "finished", Task: "T1", Model: model, Reason: reason, LimitUtilization: util,
			LimitResetAt: at.Format(time.RFC3339), LimitWindow: "five_hour"}
	}
	cases := []struct {
		name    string
		events  []Event
		pauseAt float64
		paused  bool
	}{
		{"at the threshold", []Event{fin("m", "stop", 0.96, reset)}, 0.95, true},
		{"exactly the threshold", []Event{fin("m", "stop", 0.95, reset)}, 0.95, true},
		{"below the threshold", []Event{fin("m", "stop", 0.91, reset)}, 0.95, false},
		{"lower configured threshold", []Event{fin("m", "stop", 0.91, reset)}, 0.9, true},
		{"disabled", []Event{fin("m", "stop", 0.99, reset)}, 0, false},
		{"expired reset", []Event{fin("m", "stop", 0.99, now.Add(-time.Minute))}, 0.95, false},
		{"later clean finish below", []Event{fin("m", "stop", 0.96, reset), fin("m", "stop", 0.12, reset)}, 0.95, false},
		{"later finish without an event", []Event{fin("m", "stop", 0.96, reset), {Kind: "finished", Model: "m", Reason: "stop"}}, 0.95, false},
		{"older high finish only", []Event{fin("m", "error", 0.97, reset), fin("m", "error", 0.5, reset)}, 0.95, false},
		{"a different model", []Event{fin("other", "stop", 0.99, reset)}, 0.95, false},
	}
	for _, c := range cases {
		p, paused := rateLimitPausedAt(c.events, "m", now, c.pauseAt)
		if paused != c.paused {
			t.Errorf("%s: paused = %v, want %v", c.name, paused, c.paused)
		}
		if paused && (!p.Until.Equal(reset) || p.Utilization == 0) {
			t.Errorf("%s: pause = %+v, want until %v with the utilization", c.name, p, reset)
		}
	}
	p, _ := rateLimitPausedAt([]Event{fin("m", "stop", 0.96, reset)}, "m", now, 0.95)
	if got, want := p.reason(), " (96% of the five_hour window used)"; got != want {
		t.Errorf("reason() = %q, want %q", got, want)
	}
	// A hit limit keeps its own reset and names no utilization.
	hit := fin("m", "rate-limited", 0.99, reset)
	hit.ResetAt = reset.Format(time.RFC3339)
	if p, ok := rateLimitPausedAt([]Event{hit}, "m", now, 0.95); !ok || p.reason() != "" {
		t.Errorf("hit limit: pause = %+v, %v; want paused with no utilization reason", p, ok)
	}
	// The default-threshold wrapper pauses at 0.95.
	if _, ok := rateLimitPaused([]Event{fin("m", "stop", 0.96, reset)}, "m", now); !ok {
		t.Errorf("rateLimitPaused at 0.96 = false, want paused at the default 0.95")
	}
	andon := pausedAndon([]Event{fin("m", "stop", 0.96, reset)}, now, 0.95)
	want := "paused until " + reset.Local().Format("15:04") + " (96% used)"
	if len(andon) != 1 || andon[0].Task != "model/m" || andon[0].State != want {
		t.Errorf("pausedAndon = %+v, want model/m %q", andon, want)
	}
}
