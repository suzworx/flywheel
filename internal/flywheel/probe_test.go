package flywheel

import (
	"strings"
	"testing"
	"time"
)

// TestProbeOkClosesBreaker tests that a probed ok event closes the breaker.
func TestProbeOkClosesBreaker(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	nowMinus3m := now.Add(-3 * time.Minute)
	nowMinus2m := now.Add(-2 * time.Minute)
	nowMinus1m := now.Add(-1 * time.Minute)

	events := []Event{
		{
			TS:     nowMinus3m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus2m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus1m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "probed",
			Reason: "ok",
		},
	}

	b := Breaker{Errors: 2, Cooldown: "1h"}
	open, _ := breakerOpen(events, "m1", b, now)
	if open {
		t.Errorf("breakerOpen = true, want false (probed ok should close the breaker)")
	}
}

// TestProbeBeforeErrorsIgnored tests that a probed ok before errors is ignored.
func TestProbeBeforeErrorsIgnored(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	nowMinus5m := now.Add(-5 * time.Minute)
	nowMinus3m := now.Add(-3 * time.Minute)
	nowMinus2m := now.Add(-2 * time.Minute)

	events := []Event{
		{
			TS:     nowMinus5m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "probed",
			Reason: "ok",
		},
		{
			TS:     nowMinus3m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus2m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
	}

	b := Breaker{Errors: 2, Cooldown: "1h"}
	open, _ := breakerOpen(events, "m1", b, now)
	if !open {
		t.Errorf("breakerOpen = false, want true (probed ok before errors should not close)")
	}
}

// TestProbeNotOkIgnored tests that a probed event with Reason != "ok" is ignored.
func TestProbeNotOkIgnored(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	nowMinus3m := now.Add(-3 * time.Minute)
	nowMinus2m := now.Add(-2 * time.Minute)
	nowMinus1m := now.Add(-1 * time.Minute)

	events := []Event{
		{
			TS:     nowMinus3m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus2m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus1m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "probed",
			Reason: "credits",
		},
	}

	b := Breaker{Errors: 2, Cooldown: "1h"}
	open, _ := breakerOpen(events, "m1", b, now)
	if !open {
		t.Errorf("breakerOpen = false, want true (probed non-ok should not close)")
	}
}

// TestProbeOtherModelIgnored tests that a probed event for another model is ignored.
func TestProbeOtherModelIgnored(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	nowMinus3m := now.Add(-3 * time.Minute)
	nowMinus2m := now.Add(-2 * time.Minute)
	nowMinus1m := now.Add(-1 * time.Minute)

	events := []Event{
		{
			TS:     nowMinus3m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus2m.Format(time.RFC3339Nano),
			Model:  "m1",
			Kind:   "finished",
			Reason: "error",
		},
		{
			TS:     nowMinus1m.Format(time.RFC3339Nano),
			Model:  "m2",
			Kind:   "probed",
			Reason: "ok",
		},
	}

	b := Breaker{Errors: 2, Cooldown: "1h"}
	open, _ := breakerOpen(events, "m1", b, now)
	if !open {
		t.Errorf("breakerOpen = false, want true (probed for different model should not close m1)")
	}
}

// TestProbeValidate tests Validate for probed events.
func TestProbeValidate(t *testing.T) {
	tests := []struct {
		name    string
		event   Event
		wantErr bool
	}{
		{
			name:    "probed with no model",
			event:   Event{Kind: "probed", Reason: "ok"},
			wantErr: true,
		},
		{
			name:    "probed with no reason",
			event:   Event{Kind: "probed", Model: "m1"},
			wantErr: true,
		},
		{
			name:    "probed with model and reason",
			event:   Event{Kind: "probed", Model: "m1", Reason: "ok"},
			wantErr: false,
		},
		{
			name:    "probed with model and credits reason",
			event:   Event{Kind: "probed", Model: "m2", Reason: "credits"},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.event)
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestProbeRecordProbes tests RecordProbes appends probed events.
func TestProbeRecordProbes(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	probes := []DoctorProbe{
		{Model: "m1", Class: "ok"},
		{Model: "m2", Class: "credits"},
	}

	if err := RecordProbes(dir, probes); err != nil {
		t.Fatalf("RecordProbes: %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Find the probed events (there may be other events from Init).
	var probed []Event
	for _, e := range events {
		if e.Kind == "probed" {
			probed = append(probed, e)
		}
	}

	if len(probed) != 2 {
		t.Fatalf("ReadEvents found %d probed events, want 2", len(probed))
	}

	// Check the probed events have the correct models and reasons.
	if probed[0].Model != "m1" || probed[0].Reason != "ok" {
		t.Errorf("probed[0] = {Model: %q, Reason: %q}, want {Model: m1, Reason: ok}", probed[0].Model, probed[0].Reason)
	}
	if probed[1].Model != "m2" || probed[1].Reason != "credits" {
		t.Errorf("probed[1] = {Model: %q, Reason: %q}, want {Model: m2, Reason: credits}", probed[1].Model, probed[1].Reason)
	}

	// Check the Note field.
	if probed[0].Note != "flywheel doctor" {
		t.Errorf("probed[0].Note = %q, want flywheel doctor", probed[0].Note)
	}
}

// TestProbeSummaryShowsTokensAndRate tests FactorySummary displays tokens and rate.
func TestProbeSummaryShowsTokensAndRate(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Load config and modify it to include tokens and rate limits.
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	cfg.Limits.Budget = &Budget{
		WaveCostUSD: 1.00,
		WaveTokens:  5000,
	}
	cfg.Limits.RatePerMinute = 3

	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	summary, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary: %v", err)
	}

	if !strings.Contains(summary, "tokens 5000") {
		t.Errorf("FactorySummary does not contain 'tokens 5000':\n%s", summary)
	}
	if !strings.Contains(summary, "rate 3/min") {
		t.Errorf("FactorySummary does not contain 'rate 3/min':\n%s", summary)
	}
}
