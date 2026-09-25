package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFallbackTakesOverWhenBreakerOpen checks that when a worker's model's
// breaker is open, a fresh run dispatches to the first approved fallback
// whose own breaker is closed (issue #46).
func TestFallbackTakesOverWhenBreakerOpen(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	fallbackFixture := filepath.Join(t.TempDir(), "fallback.jsonl")
	fixtureBits, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(fallbackFixture, fixtureBits, 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}
	cfg := simConfig(fixture)
	cfg.Workers[0].Fallbacks = []Fallback{{Model: fallbackFixture, Approved: true}}
	cfg.Limits.Breaker = &Breaker{Errors: 2, Cooldown: "1h"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	if err := AppendEvent(dir, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r2: %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var dispatched Event
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			dispatched = e
			break
		}
	}
	if dispatched.Kind != "dispatched" {
		t.Fatalf("no dispatched event found for T1")
	}
	if dispatched.Model != fallbackFixture {
		t.Errorf("dispatched.Model = %q, want %q", dispatched.Model, fallbackFixture)
	}
}

// TestFallbackUnapprovedStillRefused checks that an unapproved fallback is
// not used to take over when the breaker is open (issue #46).
func TestFallbackUnapprovedStillRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	fallbackFixture := filepath.Join(t.TempDir(), "fallback.jsonl")
	fixtureBits, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(fallbackFixture, fixtureBits, 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}
	cfg := simConfig(fixture)
	cfg.Workers[0].Fallbacks = []Fallback{{Model: fallbackFixture, Approved: false}}
	cfg.Limits.Breaker = &Breaker{Errors: 2, Cooldown: "1h"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	if err := AppendEvent(dir, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r2: %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "breaker" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='breaker'", err)
	}
}

// TestFallbackExplicitModelStillRefused checks that an explicit --model
// option still gets refused when the breaker is open (issue #46).
func TestFallbackExplicitModelStillRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	fallbackFixture := filepath.Join(t.TempDir(), "fallback.jsonl")
	fixtureBits, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(fallbackFixture, fixtureBits, 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}
	cfg := simConfig(fixture)
	cfg.Workers[0].Fallbacks = []Fallback{{Model: fallbackFixture, Approved: true}}
	cfg.Limits.Breaker = &Breaker{Errors: 2, Cooldown: "1h"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	if err := AppendEvent(dir, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r2: %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1", Model: fixture})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "breaker" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='breaker'", err)
	}
}

// TestFallbackAlsoOpenRefused checks that when both the default model and
// the fallback have open breakers, the dispatch is refused (issue #46).
func TestFallbackAlsoOpenRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	fallbackFixture := filepath.Join(t.TempDir(), "fallback.jsonl")
	fixtureBits, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(fallbackFixture, fixtureBits, 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}
	cfg := simConfig(fixture)
	cfg.Workers[0].Fallbacks = []Fallback{{Model: fallbackFixture, Approved: true}}
	cfg.Limits.Breaker = &Breaker{Errors: 2, Cooldown: "1h"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	if err := AppendEvent(dir, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r2: %v", err)
	}
	// Also make the fallback's breaker open
	t3 := now.Add(-20 * time.Second)
	t4 := now.Add(-10 * time.Second)
	if err := AppendEvent(dir, Event{TS: t3.Format(time.RFC3339), Task: "T3", Kind: "dispatched", Attempt: "r1", Model: fallbackFixture}); err != nil {
		t.Fatalf("AppendEvent() dispatched T3: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t3.Format(time.RFC3339), Task: "T3", Kind: "finished", Attempt: "r1", Model: fallbackFixture, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T3 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t4.Format(time.RFC3339), Task: "T3", Kind: "dispatched", Attempt: "r2", Model: fallbackFixture}); err != nil {
		t.Fatalf("AppendEvent() dispatched T3 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t4.Format(time.RFC3339), Task: "T3", Kind: "finished", Attempt: "r2", Model: fallbackFixture, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T3 r2: %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "breaker" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='breaker'", err)
	}
}

// TestFallbackReconcileDispatchesViaFallback checks that Reconcile dispatches
// via an approved fallback when the default model's breaker is open and the
// fallback's breaker is closed (issue #46).
func TestFallbackReconcileDispatchesViaFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	fixture := noPlanFixture(t, 5, false)
	fallbackFixture := filepath.Join(t.TempDir(), "fallback.jsonl")
	fixtureBits, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(fallbackFixture, fixtureBits, 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}
	p := Policy{
		MaxParallel: 1,
		Model:       "m1",
		Fallbacks:   []string{"m2"},
		Breaker:     &Breaker{Errors: 2, Cooldown: "10m"},
	}
	now := time.Now()
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	var events []Event
	events = append(events, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T1", Kind: "planned", Brief: "b.txt"})
	events = append(events, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: "m1"})
	events = append(events, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"})
	events = append(events, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: "m1"})
	events = append(events, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: "m1", Reason: "error"})
	state := Derive(events)
	obs := Observed{Leases: []Lease{}}
	actions := Reconcile(state, events, obs, p, now)
	var dispatch *Action
	for i := range actions {
		if actions[i].Task == "T1" {
			dispatch = &actions[i]
			break
		}
	}
	if dispatch == nil {
		t.Fatalf("no action for T1")
	}
	if dispatch.Kind != "DISPATCH" {
		t.Errorf("dispatch.Kind = %q, want DISPATCH", dispatch.Kind)
	}
	if !strings.Contains(dispatch.Reason, "approved fallback m2 takes over") {
		t.Errorf("dispatch.Reason = %q, want to contain 'approved fallback m2 takes over'", dispatch.Reason)
	}
}
