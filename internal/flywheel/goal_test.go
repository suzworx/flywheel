package flywheel

import (
	"reflect"
	"testing"
)

// goalEvent is a goal event carrying one spec.
func goalEvent(ts, id, title, status string, required []string) Event {
	return Event{TS: ts, Kind: "goal", Goal: &GoalSpec{ID: id, Title: title, Required: required, Status: status}}
}

// taskEvent returns an event for a task; tsk is "" when the event is a goal.
func taskEvent(ts, task, kind, verdict string) Event {
	e := Event{TS: ts, Task: task, Kind: kind}
	if verdict != "" {
		e.Verdict = verdict
	}
	return e
}

// linkedPlanned returns a planned event linking task to goal.
func linkedPlanned(ts, task, goal string) Event {
	e := Event{TS: ts, Task: task, Kind: "planned"}
	if goal != "" {
		e.GoalID = goal
	}
	return e
}

func progressEvents() []Event {
	return []Event{
		goalEvent("2026-09-01T00:00:00Z", "g1", "Ship status", "active", []string{"t1", "t2", "t3"}),
		goalEvent("2026-09-01T00:00:01Z", "g0", "Launch", "active", []string{"t0"}),
		// t1 accepted (passed).
		taskEvent("2026-09-01T00:01:00Z", "t1", "planned", ""),
		taskEvent("2026-09-01T00:01:01Z", "t1", "dispatched", ""),
		taskEvent("2026-09-01T00:01:02Z", "t1", "started", ""),
		taskEvent("2026-09-01T00:01:03Z", "t1", "finished", ""),
		taskEvent("2026-09-01T00:01:04Z", "t1", "reviewed", "pass"),
		// t2 in flight (dispatched).
		taskEvent("2026-09-01T00:02:00Z", "t2", "planned", ""),
		taskEvent("2026-09-01T00:02:01Z", "t2", "dispatched", ""),
		// t3 not started (planned).
		taskEvent("2026-09-01T00:03:00Z", "t3", "planned", ""),
		// t4 linked via planned goal id; never dispatched (planned).
		linkedPlanned("2026-09-01T00:04:00Z", "t4", "g1"),
		// t5 blocked.
		linkedPlanned("2026-09-01T00:05:00Z", "t5", "g1"),
		taskEvent("2026-09-01T00:05:01Z", "t5", "dispatched", ""),
		taskEvent("2026-09-01T00:05:02Z", "t5", "blocked", ""),
		// t6 in flight (needs-correction).
		linkedPlanned("2026-09-01T00:06:00Z", "t6", "g1"),
		taskEvent("2026-09-01T00:06:01Z", "t6", "dispatched", ""),
		taskEvent("2026-09-01T00:06:02Z", "t6", "started", ""),
		taskEvent("2026-09-01T00:06:03Z", "t6", "finished", ""),
		taskEvent("2026-09-01T00:06:04Z", "t6", "reviewed", "correct"),
		// t7 accepted (landed).
		linkedPlanned("2026-09-01T00:07:00Z", "t7", "g1"),
		taskEvent("2026-09-01T00:07:01Z", "t7", "dispatched", ""),
		taskEvent("2026-09-01T00:07:02Z", "t7", "started", ""),
		taskEvent("2026-09-01T00:07:03Z", "t7", "finished", ""),
		taskEvent("2026-09-01T00:07:04Z", "t7", "reviewed", "pass"),
		taskEvent("2026-09-01T00:07:05Z", "t7", "landed", ""),
		// t8 blocked (rejected).
		linkedPlanned("2026-09-01T00:08:00Z", "t8", "g1"),
		taskEvent("2026-09-01T00:08:01Z", "t8", "dispatched", ""),
		taskEvent("2026-09-01T00:08:02Z", "t8", "started", ""),
		taskEvent("2026-09-01T00:08:03Z", "t8", "finished", ""),
		taskEvent("2026-09-01T00:08:04Z", "t8", "reviewed", "reject"),
		// t0's own linked planned event, so g0 requires t0 plus linked t9.
		linkedPlanned("2026-09-01T00:09:00Z", "t0", "g0"),
		linkedPlanned("2026-09-01T00:09:01Z", "t9", "g0"),
	}
}

func TestGoalsProgressCounts(t *testing.T) {
	t.Parallel()
	gs := Goals(progressEvents())
	if len(gs) != 2 {
		t.Fatalf("Goals() = %d views, want 2", len(gs))
	}
	g := gs[0] // g0 sorts before g1
	if g.ID != "g0" {
		t.Fatalf("Goals()[0].ID = %q, want g0", g.ID)
	}
	if g.Total != 2 || g.Accepted != 0 || g.InFlight != 0 || g.NotStarted != 2 || g.Blocked != 0 {
		t.Errorf("g0 counts = total %d, accepted %d, in-flight %d, not-started %d, blocked %d; want 2/0/0/2/0",
			g.Total, g.Accepted, g.InFlight, g.NotStarted, g.Blocked)
	}
	if g.Progress != "0/2 required tasks accepted" {
		t.Errorf("g0 progress = %q, want 0/2 required tasks accepted", g.Progress)
	}

	g = gs[1]
	if g.ID != "g1" {
		t.Fatalf("Goals()[1].ID = %q, want g1", g.ID)
	}
	// Required: goal's own t1, t2, t3 plus linked t4, t5, t6, t7, t8.
	if g.Total != 8 {
		t.Errorf("g1 total = %d, want 8", g.Total)
	}
	if g.Accepted != 2 || g.InFlight != 2 || g.NotStarted != 2 || g.Blocked != 2 {
		t.Errorf("g1 counts = accepted %d, in-flight %d, not-started %d, blocked %d; want 2/2/2/2",
			g.Accepted, g.InFlight, g.NotStarted, g.Blocked)
	}
	if g.Progress != "2/8 required tasks accepted" {
		t.Errorf("g1 progress = %q, want 2/8 required tasks accepted", g.Progress)
	}
	if want := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"}; !reflect.DeepEqual(g.Required, want) {
		t.Errorf("g1 required = %v, want %v", g.Required, want)
	}
}

func TestGoalsLatestWinsAndCreatedAt(t *testing.T) {
	t.Parallel()
	events := []Event{
		goalEvent("2026-09-01T00:00:00Z", "g1", "Old title", "active", []string{"t1"}),
		goalEvent("2026-09-02T00:00:00Z", "g1", "Ship status", "met", []string{"t1", "t2"}),
	}
	gs := Goals(events)
	if len(gs) != 1 {
		t.Fatalf("Goals() = %d views, want 1", len(gs))
	}
	g := gs[0]
	if g.Title != "Ship status" {
		t.Errorf("g1 title = %q, want Ship status (latest wins)", g.Title)
	}
	if g.Status != "met" {
		t.Errorf("g1 status = %q, want met (latest wins)", g.Status)
	}
	if g.CreatedAt != "2026-09-01T00:00:00Z" {
		t.Errorf("g1 created_at = %q, want the first event's ts", g.CreatedAt)
	}
	if !reflect.DeepEqual(g.Required, []string{"t1", "t2"}) {
		t.Errorf("g1 required = %v, want [t1 t2] (latest wins)", g.Required)
	}
}

func TestGoalsSortsById(t *testing.T) {
	t.Parallel()
	events := []Event{
		goalEvent("2026-09-01T00:00:00Z", "g2", "Two", "active", nil),
		goalEvent("2026-09-01T00:00:01Z", "g1", "One", "active", nil),
		goalEvent("2026-09-01T00:00:02Z", "g10", "Ten", "active", nil),
	}
	gs := Goals(events)
	want := []string{"g1", "g10", "g2"}
	if len(gs) != 3 {
		t.Fatalf("Goals() = %d views, want 3", len(gs))
	}
	for i, w := range want {
		if gs[i].ID != w {
			t.Errorf("Goals()[%d].ID = %q, want %q", i, gs[i].ID, w)
		}
	}
}
