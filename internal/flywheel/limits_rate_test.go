package flywheel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestLimitsTokensRefusesDispatch checks that Run refuses a dispatch when the
// recorded tokens have reached limits.budget.wave_tokens.
func TestLimitsTokensRefusesDispatch(t *testing.T) {
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	cfg := simConfig(fixture)
	cfg.Limits.Budget = &Budget{WaveTokens: 100}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add a finished event with tokens summing >= 100
	now := limitsTestNow
	setTestNow(t, now)
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T2", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: now.Format(time.RFC3339Nano), Task: "T2", Kind: "dispatched", Attempt: "r1",
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched: %v", err)
	}
	tokens := &Tokens{Input: 80, Output: 30}
	if err := AppendEvent(dir, Event{
		TS: now.Format(time.RFC3339Nano), Task: "T2", Kind: "finished", Attempt: "r1",
		Reason: "stop", Tokens: tokens,
	}); err != nil {
		t.Fatalf("AppendEvent() finished: %v", err)
	}
	// Try to run T1; should be refused with budget RuleRefusal
	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var re *RuleRefusal
	if !errors.As(err, &re) || re.Rule != "budget" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='budget'", err)
	}
	if !strings.Contains(re.Fix, "wave_tokens") {
		t.Errorf("re.Fix = %q, want to mention wave_tokens", re.Fix)
	}
}

// TestLimitsTokensCacheDoesNotCount checks that cache read/write tokens do
// not count toward the wave token budget.
func TestLimitsTokensCacheDoesNotCount(t *testing.T) {
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	cfg := simConfig(fixture)
	cfg.Limits.Budget = &Budget{WaveTokens: 100}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add a finished event with high cache tokens but low billable tokens
	now := limitsTestNow
	setTestNow(t, now)
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T2", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: now.Format(time.RFC3339Nano), Task: "T2", Kind: "dispatched", Attempt: "r1",
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched: %v", err)
	}
	tokens := &Tokens{Input: 10, CacheRead: 500}
	if err := AppendEvent(dir, Event{
		TS: now.Format(time.RFC3339Nano), Task: "T2", Kind: "finished", Attempt: "r1",
		Reason: "stop", Tokens: tokens,
	}); err != nil {
		t.Fatalf("AppendEvent() finished: %v", err)
	}
	// Run should succeed because only 10 tokens count (not 510)
	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stop" {
		t.Errorf("res.Reason = %q, want \"stop\"", res.Reason)
	}
}

// TestLimitsRateRefusesBurst checks that Run refuses a dispatch when the
// model has already been dispatched the rate_per_minute limit in the last 60s.
func TestLimitsRateRefusesBurst(t *testing.T) {
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	cfg := simConfig(fixture)
	cfg.Limits.RatePerMinute = 2
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := limitsTestNow
	setTestNow(t, now)
	// Add two recent dispatched events for the same model
	t1 := now.Add(-20 * time.Second)
	t2 := now.Add(-10 * time.Second)
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T2", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: t1.Format(time.RFC3339Nano), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model,
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T3", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned T3: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: t2.Format(time.RFC3339Nano), Task: "T3", Kind: "dispatched", Attempt: "r1", Model: model,
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched T3 r1: %v", err)
	}
	// Try to run T1; should be refused with rate RuleRefusal
	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var re *RuleRefusal
	if !errors.As(err, &re) || re.Rule != "rate" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='rate'", err)
	}
}

// TestLimitsRateWindowSlides checks that dispatches older than 60 seconds
// do not count toward the rate limit.
func TestLimitsRateWindowSlides(t *testing.T) {
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	cfg := simConfig(fixture)
	cfg.Limits.RatePerMinute = 2
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := limitsTestNow
	setTestNow(t, now)
	// Add two old dispatched events outside the 60s window
	t1 := now.Add(-90 * time.Second)
	t2 := now.Add(-70 * time.Second)
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T2", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: t1.Format(time.RFC3339Nano), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model,
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: "", Task: "T3", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent() planned T3: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: t2.Format(time.RFC3339Nano), Task: "T3", Kind: "dispatched", Attempt: "r1", Model: model,
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched T3 r1: %v", err)
	}
	// Run should succeed because both dispatches are outside the 60s window
	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stop" {
		t.Errorf("res.Reason = %q, want \"stop\"", res.Reason)
	}
}

// TestLimitsTokensReconcileHolds checks that Reconcile holds a task when
// the recorded tokens have reached the budget.
func TestLimitsTokensReconcileHolds(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "b.txt"},
		{Task: "T2", Kind: "planned", Brief: "b.txt"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Model: "sim"},
		{Task: "T2", Kind: "finished", Attempt: "r1", Reason: "stop", Tokens: &Tokens{Input: 150}},
	}
	s := Derive(events)
	p := Policy{MaxParallel: 2, Model: "sim", BudgetTokens: 100}
	actions := Reconcile(s, events, Observed{}, p, limitsTestNow)

	holding := false
	for _, a := range actions {
		if a.Task == "T1" && a.Kind == "HOLD" && strings.Contains(a.Reason, "wave_tokens") {
			holding = true
			break
		}
	}
	if !holding {
		t.Errorf("Reconcile() did not HOLD T1 due to wave_tokens; actions = %+v", actions)
	}
}

// TestLimitsRateReconcileCapsCapacity checks that Reconcile caps capacity
// based on the model's dispatch rate.
func TestLimitsRateReconcileCapsCapacity(t *testing.T) {
	now := limitsTestNow
	setTestNow(t, now)
	beforeWindow := now.Add(-10 * time.Second)
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "b.txt"},
		{Task: "T2", Kind: "planned", Brief: "b.txt"},
		{Task: "T3", Kind: "planned", Brief: "b.txt"},
		{Task: "T4", Kind: "planned", Brief: "b.txt"},
		// One m1 dispatch 10s ago (within window)
		{TS: beforeWindow.Format(time.RFC3339Nano), Task: "T5", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		// Finished event so the lease expires
		{TS: beforeWindow.Format(time.RFC3339Nano), Task: "T5", Kind: "finished", Attempt: "r1", Reason: "stop"},
	}
	s := Derive(events)
	p := Policy{MaxParallel: 4, Model: "m1", RatePerMinute: 2}
	actions := Reconcile(s, events, Observed{}, p, now)

	dispatchCount := 0
	for _, a := range actions {
		if a.Kind == "DISPATCH" {
			dispatchCount++
		}
	}
	if dispatchCount != 1 {
		t.Errorf("Reconcile() dispatched %d tasks, want 1 (capacity capped to 2-1=1); actions = %+v", dispatchCount, actions)
	}
}

// TestLimitsRateValidate checks that Config.Validate rejects invalid rate and
// token budget values.
func TestLimitsRateValidate(t *testing.T) {
	tests := []struct {
		name    string
		limits  Limits
		wantErr bool
		wantMsg string
	}{
		{
			name:    "negative rate per minute",
			limits:  Limits{RatePerMinute: -1},
			wantErr: true,
			wantMsg: "rate_per_minute",
		},
		{
			name:    "negative wave tokens",
			limits:  Limits{Budget: &Budget{WaveTokens: -1}},
			wantErr: true,
			wantMsg: "wave_tokens",
		},
		{
			name:    "valid zero values",
			limits:  Limits{RatePerMinute: 0, Budget: &Budget{WaveTokens: 0}},
			wantErr: false,
		},
		{
			name:    "valid positive values",
			limits:  Limits{RatePerMinute: 2, Budget: &Budget{WaveTokens: 100}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Version: 1,
				Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
				Limits:  tt.limits,
			}
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Validate() error = %v, want message containing %q", err, tt.wantMsg)
			}
		})
	}
}

// limitsTestNow is the fixed instant the limits tests run at: Run reads the
// injectable clock now, so the rate window is deterministic (#329 review).
var limitsTestNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// setTestNow points the package clock at ts for the rest of the test.
func setTestNow(t *testing.T, ts time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return ts }
	t.Cleanup(func() { now = old })
}

// TestLimitsRateAppliesToFallback checks that when an approved fallback takes
// over, next counts the fallback's own dispatches against the rate limit, as
// run does (#329 review).
func TestLimitsRateAppliesToFallback(t *testing.T) {
	ts := func(d time.Duration) string { return limitsTestNow.Add(d).Format(time.RFC3339Nano) }
	events := []Event{
		{TS: ts(-5 * time.Minute), Task: "E1", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: ts(-4 * time.Minute), Task: "E2", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: ts(-30 * time.Second), Task: "F1", Kind: "dispatched", Attempt: "r1", Model: "m2"},
		{TS: ts(-29 * time.Second), Task: "F1", Kind: "finished", Attempt: "r1", Model: "m2", Reason: "stop"},
		{TS: ts(-20 * time.Second), Task: "F2", Kind: "dispatched", Attempt: "r1", Model: "m2"},
		{TS: ts(-19 * time.Second), Task: "F2", Kind: "finished", Attempt: "r1", Model: "m2", Reason: "stop"},
		{TS: ts(-10 * time.Second), Task: "T1", Kind: "planned", Brief: "b.txt"},
	}
	p := Policy{MaxParallel: 4, Model: "m1", Fallbacks: []string{"m2"},
		Breaker: &Breaker{Errors: 2, Cooldown: "10m"}, RatePerMinute: 2}
	for _, a := range Reconcile(Derive(events), events, Observed{}, p, limitsTestNow) {
		if a.Kind == "DISPATCH" {
			t.Errorf("DISPATCH %s while fallback m2 is at its rate limit, want none", a.Task)
		}
	}
}
