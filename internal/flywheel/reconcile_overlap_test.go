package flywheel

import (
	"testing"
	"time"
)

func TestReconcileOverlapSameOwnsOneDispatch(t *testing.T) {
	// T1, T2 both own a.go → DISPATCH T1, WAIT T2 with Reason "owns a.go overlaps T1".
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header := &BriefHeader{
		Owns: []string{"a.go"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 4}, now)

	var t1Action, t2Action Action
	for _, a := range actions {
		if a.Task == "T1" {
			t1Action = a
		}
		if a.Task == "T2" {
			t2Action = a
		}
	}

	if t1Action.Kind != "DISPATCH" {
		t.Errorf("T1: want DISPATCH, got %s", t1Action.Kind)
	}
	if t2Action.Kind != "WAIT" {
		t.Errorf("T2: want WAIT, got %s", t2Action.Kind)
	}
	if t2Action.Reason != "owns a.go overlaps T1" {
		t.Errorf("T2: want reason 'owns a.go overlaps T1', got %q", t2Action.Reason)
	}
}

func TestReconcileOverlapWithRunningTask(t *testing.T) {
	// T1 dispatched (owns a.go, status dispatched), T2 planned owns a.go → T2 WAIT "owns a.go overlaps T1".
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header1 := &BriefHeader{
		Owns: []string{"a.go"},
	}
	header2 := &BriefHeader{
		Owns: []string{"a.go"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header1},
		{TS: ts, Task: "T1", Kind: "dispatched", Brief: "b1", Attempt: "a1", Header: header1},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header2},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 4}, now)

	var t2Action Action
	for _, a := range actions {
		if a.Task == "T2" {
			t2Action = a
		}
	}

	if t2Action.Kind != "WAIT" {
		t.Errorf("T2: want WAIT, got %s", t2Action.Kind)
	}
	if t2Action.Reason != "owns a.go overlaps T1" {
		t.Errorf("T2: want reason 'owns a.go overlaps T1', got %q", t2Action.Reason)
	}
}

func TestReconcileOverlapDirPrefix(t *testing.T) {
	// T1 owns "internal/", T2 owns "internal/x.go" → T2 WAIT.
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header1 := &BriefHeader{
		Owns: []string{"internal/"},
	}
	header2 := &BriefHeader{
		Owns: []string{"internal/x.go"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header1},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header2},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 4}, now)

	var t2Action Action
	for _, a := range actions {
		if a.Task == "T2" {
			t2Action = a
		}
	}

	if t2Action.Kind != "WAIT" {
		t.Errorf("T2: want WAIT, got %s", t2Action.Kind)
	}
	if t2Action.Reason != "owns internal/x.go overlaps T1" && t2Action.Reason != "owns internal/ overlaps T1" {
		t.Errorf("T2: want overlap reason, got %q", t2Action.Reason)
	}
}

func TestReconcileOverlapExclusive(t *testing.T) {
	// T1 and T2 own different files but both exclusive "db" → T2 WAIT "exclusive db overlaps T1".
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header1 := &BriefHeader{
		Owns:      []string{"a.go"},
		Exclusive: []string{"db"},
	}
	header2 := &BriefHeader{
		Owns:      []string{"b.go"},
		Exclusive: []string{"db"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header1},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header2},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 4}, now)

	var t2Action Action
	for _, a := range actions {
		if a.Task == "T2" {
			t2Action = a
		}
	}

	if t2Action.Kind != "WAIT" {
		t.Errorf("T2: want WAIT, got %s", t2Action.Kind)
	}
	if t2Action.Reason != "exclusive db overlaps T1" {
		t.Errorf("T2: want reason 'exclusive db overlaps T1', got %q", t2Action.Reason)
	}
}

func TestReconcileOverlapDisjointBothDispatch(t *testing.T) {
	// T1 owns a.go, T2 owns b.go → both DISPATCH.
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header1 := &BriefHeader{
		Owns: []string{"a.go"},
	}
	header2 := &BriefHeader{
		Owns: []string{"b.go"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header1},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header2},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 4}, now)

	var t1Action, t2Action Action
	for _, a := range actions {
		if a.Task == "T1" {
			t1Action = a
		}
		if a.Task == "T2" {
			t2Action = a
		}
	}

	if t1Action.Kind != "DISPATCH" {
		t.Errorf("T1: want DISPATCH, got %s", t1Action.Kind)
	}
	if t2Action.Kind != "DISPATCH" {
		t.Errorf("T2: want DISPATCH, got %s", t2Action.Kind)
	}
}

func TestReconcileOverlapCapacityCountsChosenOnly(t *testing.T) {
	// MaxParallel 1; T1 and T2 own a.go, T3 owns c.go
	// → DISPATCH T1 only, WAIT T2 (overlap); T3 is cut by capacity (not in the output), as today.
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339Nano)
	header1 := &BriefHeader{
		Owns: []string{"a.go"},
	}
	header2 := &BriefHeader{
		Owns: []string{"a.go"},
	}
	header3 := &BriefHeader{
		Owns: []string{"c.go"},
	}
	events := []Event{
		{TS: ts, Task: "T1", Kind: "planned", Brief: "b1", Header: header1},
		{TS: ts, Task: "T2", Kind: "planned", Brief: "b2", Header: header2},
		{TS: ts, Task: "T3", Kind: "planned", Brief: "b3", Header: header3},
	}
	state := Derive(events)
	actions := Reconcile(state, events, Observed{}, Policy{MaxParallel: 1}, now)

	var t1Action, t2Action, t3Action Action
	for _, a := range actions {
		if a.Task == "T1" {
			t1Action = a
		}
		if a.Task == "T2" {
			t2Action = a
		}
		if a.Task == "T3" {
			t3Action = a
		}
	}

	if t1Action.Kind != "DISPATCH" {
		t.Errorf("T1: want DISPATCH, got %s", t1Action.Kind)
	}
	if t2Action.Kind != "WAIT" {
		t.Errorf("T2: want WAIT, got %s", t2Action.Kind)
	}
	if t3Action.Kind != "" {
		// T3 should not be in actions (capacity cut from chosen only, not from WAITs)
		t.Errorf("T3: want no action, got %s", t3Action.Kind)
	}
}

// actionFor returns the action for task in acts (zero Action when absent).
func actionFor(acts []Action, task string) Action {
	for _, a := range acts {
		if a.Task == task {
			return a
		}
	}
	return Action{}
}

// TestReconcileOverlapCorrectionKeepsBaseOwns checks that a correction whose
// delta owns only b.go still holds its base brief's a.go, as AttemptBrief
// resolves it for run's collision check (#326 review).
func TestReconcileOverlapCorrectionKeepsBaseOwns(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := func(m int) string { return now.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	events := []Event{
		{TS: ts(1), Task: "T1", Kind: "planned", Brief: "b1", Header: &BriefHeader{Owns: []string{"a.go"}}},
		{TS: ts(2), Task: "T1", Kind: "dispatched", Attempt: "c1", Header: &BriefHeader{Owns: []string{"b.go"}}},
		{TS: ts(3), Task: "T2", Kind: "planned", Brief: "b2", Header: &BriefHeader{Owns: []string{"a.go"}}},
	}
	acts := Reconcile(Derive(events), events, Observed{}, Policy{MaxParallel: 4}, now.Add(time.Hour))
	if a := actionFor(acts, "T2"); a.Kind != "WAIT" || a.Reason != "owns a.go overlaps T1" {
		t.Errorf("T2 = %+v, want WAIT owns a.go overlaps T1", a)
	}
}

// TestReconcileOverlapHeadlessDispatchKeepsPlannedOwns checks that a legacy
// dispatched event without a header does not erase the planned owns.
func TestReconcileOverlapHeadlessDispatchKeepsPlannedOwns(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := func(m int) string { return now.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	events := []Event{
		{TS: ts(1), Task: "T1", Kind: "planned", Brief: "b1", Header: &BriefHeader{Owns: []string{"a.go"}}},
		{TS: ts(2), Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: ts(3), Task: "T2", Kind: "planned", Brief: "b2", Header: &BriefHeader{Owns: []string{"a.go"}}},
	}
	acts := Reconcile(Derive(events), events, Observed{}, Policy{MaxParallel: 4}, now.Add(time.Hour))
	if a := actionFor(acts, "T2"); a.Kind != "WAIT" {
		t.Errorf("T2 = %+v, want WAIT on T1's planned a.go", a)
	}
}

// TestReconcileOverlapHoldMakesNoFalseWait checks that a task HELD by policy
// does not make an overlapping task WAIT: neither runs this tick, and both
// keep the hold (#326 review).
func TestReconcileOverlapHoldMakesNoFalseWait(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := func(m int) string { return now.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	events := []Event{
		{TS: ts(1), Task: "T0", Kind: "finished", Attempt: "r1", Cost: 5},
		{TS: ts(2), Task: "T1", Kind: "planned", Brief: "b1", Header: &BriefHeader{Owns: []string{"a.go"}}},
		{TS: ts(3), Task: "T2", Kind: "planned", Brief: "b2", Header: &BriefHeader{Owns: []string{"a.go"}}},
	}
	acts := Reconcile(Derive(events), events, Observed{}, Policy{MaxParallel: 2, BudgetUSD: 1}, now.Add(time.Hour))
	for _, task := range []string{"T1", "T2"} {
		if a := actionFor(acts, task); a.Kind != "HOLD" {
			t.Errorf("%s = %+v, want HOLD (budget spent)", task, a)
		}
	}
}
