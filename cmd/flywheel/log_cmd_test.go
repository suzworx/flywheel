package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestAppendEventsPlannedTakesNoFeedbackLock checks a batch with no learning
// or dismissed event takes no feedback lock at all: logging a planned event
// must never wait on a feedback command, so the lock file never appears
// (issue #260).
func TestAppendEventsPlannedTakesNoFeedbackLock(t *testing.T) {
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback.lock")); !os.IsNotExist(err) {
		t.Errorf("feedback.lock appeared for a planned event (stat err=%v)", err)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != "planned" {
		t.Errorf("events = %+v, want the single planned event", events)
	}
}

// TestAppendEventsLearningRebuildsArtifact checks a batch carrying a learning
// event routes through the feedback transaction: the event is appended, the
// artifact is rebuilt from the log, and the lock is released again (no lock
// file remains).
func TestAppendEventsLearningRebuildsArtifact(t *testing.T) {
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
	}, true)
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "learnings.md"))
	if err != nil {
		t.Fatalf("read learnings.md: %v", err)
	}
	if !strings.Contains(string(b), "## L-01 — Terse") {
		t.Errorf("learnings.md lacks the imported learning:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback.lock")); !os.IsNotExist(err) {
		t.Errorf("feedback.lock still present after appendEvents (stat err=%v)", err)
	}
}

// TestAppendEventsMixedBatchKeepsBatchSemantics checks a batch carrying a
// learning event imports the whole batch — plain and learning events together
// — as one transaction, not one locked mutation per event.
func TestAppendEventsMixedBatchKeepsBatchSemantics(t *testing.T) {
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt"},
		{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
	}, true)
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want both batch events appended", events)
	}
	if events[0].Kind != "planned" || events[1].Kind != "learning" {
		t.Errorf("events = %+v, want the batch order preserved", events)
	}
}

// TestBatchHasLearning checks the routing predicate: only batches carrying a
// learning or dismissed event are routed through the feedback transaction.
func TestBatchHasLearning(t *testing.T) {
	plain := []flywheel.Event{{Kind: "planned"}, {Kind: "finished"}}
	if batchHasLearning(plain) {
		t.Error("batchHasLearning() = true for a batch with no learning or dismissed event")
	}
	withLearning := []flywheel.Event{{Kind: "planned"}, {Kind: "learning"}}
	if !batchHasLearning(withLearning) {
		t.Error("batchHasLearning() = false for a batch carrying a learning event")
	}
	withDismissed := []flywheel.Event{{Kind: "dismissed"}}
	if !batchHasLearning(withDismissed) {
		t.Error("batchHasLearning() = false for a batch carrying a dismissed event")
	}
}
