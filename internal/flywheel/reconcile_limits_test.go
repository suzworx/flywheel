package flywheel

import (
	"strings"
	"testing"
	"time"
)

// TestReconcileLimitsDispatchUnderBudget checks dispatch when budget is not spent.
func TestReconcileLimitsDispatchUnderBudget(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 10, 0, 0, time.UTC)
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:01:00Z", Task: "t1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:02:00Z", Task: "t1", Kind: "finished", Attempt: "r1", Cost: 0.4},
		{TS: "2026-09-14T00:03:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
	}
	st := Derive(events)
	obs := Observed{Leases: []Lease{}}
	p := Policy{MaxParallel: 2, BudgetUSD: 1.0}
	acts := Reconcile(st, events, obs, p, now)

	if len(acts) != 1 || acts[0].Kind != "DISPATCH" || acts[0].Task != "t2" {
		t.Errorf("TestReconcileLimitsDispatchUnderBudget: got %v, want DISPATCH for T2", acts)
	}
}

// TestReconcileLimitsHoldOnSpentBudget checks that HOLD replaces DISPATCH when budget is spent.
func TestReconcileLimitsHoldOnSpentBudget(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 10, 0, 0, time.UTC)
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:01:00Z", Task: "t1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:02:00Z", Task: "t1", Kind: "finished", Attempt: "r1", Cost: 0.6},
		{TS: "2026-09-14T00:03:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
	}
	st := Derive(events)
	obs := Observed{Leases: []Lease{}}
	p := Policy{MaxParallel: 2, BudgetUSD: 0.5}
	acts := Reconcile(st, events, obs, p, now)

	if len(acts) != 1 {
		t.Fatalf("TestReconcileLimitsHoldOnSpentBudget: got %d actions, want 1", len(acts))
	}
	if acts[0].Kind != "HOLD" || acts[0].Task != "t2" {
		t.Errorf("TestReconcileLimitsHoldOnSpentBudget: got %v, want HOLD for T2", acts[0])
	}
	if len(acts[0].Reason) == 0 || acts[0].Reason[0:6] != "budget" {
		t.Errorf("TestReconcileLimitsHoldOnSpentBudget: reason %q does not start with 'budget'", acts[0].Reason)
	}
}

// TestReconcileLimitsHoldOnOpenBreaker checks HOLD when the breaker is open.
func TestReconcileLimitsHoldOnOpenBreaker(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 10, 0, 0, time.UTC)
	errTime1 := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	errTime2 := now.Add(-1 * time.Minute).Format(time.RFC3339Nano)

	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: errTime1, Task: "t1", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: errTime1, Task: "t1", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: "2026-09-14T00:03:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
		{TS: errTime2, Task: "t2", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: errTime2, Task: "t2", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: "2026-09-14T00:08:00Z", Task: "t3", Kind: "planned", Brief: "b.txt"},
	}
	st := Derive(events)
	obs := Observed{Leases: []Lease{}}
	p := Policy{
		MaxParallel: 2,
		Model:       "m1",
		Breaker:     &Breaker{Errors: 2, Cooldown: "10m"},
	}
	acts := Reconcile(st, events, obs, p, now)

	if len(acts) != 1 {
		t.Fatalf("TestReconcileLimitsHoldOnOpenBreaker: got %d actions, want 1", len(acts))
	}
	if acts[0].Kind != "HOLD" || acts[0].Task != "t3" {
		t.Errorf("TestReconcileLimitsHoldOnOpenBreaker: got %v, want HOLD for T3", acts[0])
	}
	if len(acts[0].Reason) == 0 || acts[0].Reason[0:7] != "breaker" {
		t.Errorf("TestReconcileLimitsHoldOnOpenBreaker: reason %q does not start with 'breaker'", acts[0].Reason)
	}
	if !strings.Contains(acts[0].Reason, "m1") {
		t.Errorf("TestReconcileLimitsHoldOnOpenBreaker: reason does not contain 'm1'")
	}
}

// TestReconcileLimitsDispatchAfterCooldown checks dispatch after the breaker cooldown expires.
func TestReconcileLimitsDispatchAfterCooldown(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 40, 0, 0, time.UTC)
	errTime1 := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	errTime2 := now.Add(-20 * time.Minute).Format(time.RFC3339Nano)

	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: errTime1, Task: "t1", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: errTime1, Task: "t1", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: "2026-09-14T00:03:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
		{TS: errTime2, Task: "t2", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: errTime2, Task: "t2", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "error"},
		{TS: "2026-09-14T00:38:00Z", Task: "t3", Kind: "planned", Brief: "b.txt"},
	}
	st := Derive(events)
	obs := Observed{Leases: []Lease{}}
	p := Policy{
		MaxParallel: 2,
		Model:       "m1",
		Breaker:     &Breaker{Errors: 2, Cooldown: "10m"},
	}
	acts := Reconcile(st, events, obs, p, now)

	if len(acts) != 1 || acts[0].Kind != "DISPATCH" || acts[0].Task != "t3" {
		t.Errorf("TestReconcileLimitsDispatchAfterCooldown: got %v, want DISPATCH for T3", acts)
	}
}

// TestReconcileLimitsPolicyFromConfig checks PolicyFromConfig fills Model, BudgetUSD, and Breaker.
func TestReconcileLimitsPolicyFromConfig(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m1", MaxParallel: 2}},
		Limits: Limits{
			Budget:  &Budget{WaveCostUSD: 2.0},
			Breaker: &Breaker{Errors: 3, Cooldown: "5m"},
		},
	}
	p := PolicyFromConfig(cfg)

	if p.Model != "m1" {
		t.Errorf("PolicyFromConfig Model: got %q, want m1", p.Model)
	}
	if p.BudgetUSD != 2.0 {
		t.Errorf("PolicyFromConfig BudgetUSD: got %v, want 2.0", p.BudgetUSD)
	}
	if p.Breaker == nil || p.Breaker.Errors != 3 {
		t.Errorf("PolicyFromConfig Breaker: got %v, want {Errors: 3, ...}", p.Breaker)
	}

	// Test with no limits
	cfg2 := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m2", MaxParallel: 3}},
	}
	p2 := PolicyFromConfig(cfg2)
	if p2.BudgetUSD != 0 {
		t.Errorf("PolicyFromConfig no budget: got %v, want 0", p2.BudgetUSD)
	}
	if p2.Breaker != nil {
		t.Errorf("PolicyFromConfig no breaker: got %v, want nil", p2.Breaker)
	}
}
