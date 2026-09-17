package main

import (
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestFindGoalResolvesRecordedGoal checks findGoal returns the goal view for
// an id that was recorded with a goal event.
func TestFindGoalResolvesRecordedGoal(t *testing.T) {
	dir := t.TempDir()
	if err := flywheel.AppendEvent(dir, flywheel.Event{
		Kind: "goal",
		Goal: &flywheel.GoalSpec{ID: "goal-1", Title: "Ship it", Status: "active"},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	g, ok := findGoal(dir, "goal-1")
	if !ok {
		t.Fatal("findGoal(goal-1) = not found, want found")
	}
	if g.Title != "Ship it" {
		t.Errorf("findGoal(goal-1).Title = %q, want %q", g.Title, "Ship it")
	}
}

// TestFindGoalRejectsUnknownID checks findGoal reports not-found for an id
// that was never recorded, even while other goals exist (issue #127).
func TestFindGoalRejectsUnknownID(t *testing.T) {
	dir := t.TempDir()
	if err := flywheel.AppendEvent(dir, flywheel.Event{
		Kind: "goal",
		Goal: &flywheel.GoalSpec{ID: "goal-1", Title: "Ship it", Status: "active"},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, ok := findGoal(dir, "nope"); ok {
		t.Fatal("findGoal(nope) = found, want not found")
	}
}
