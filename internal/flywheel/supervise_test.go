package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSuperviseNeedsMeasuringFinishedOnly(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1"},
		{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: intPtr(0)},
		{TS: "2026-09-12T00:00:00Z", Task: "T2", Kind: "planned", Brief: "brief.txt"},
		{TS: "2026-09-12T00:30:00Z", Task: "T2", Kind: "dispatched", Attempt: "r1", Session: "w1"},
	}

	result := NeedsMeasuring(events)
	if len(result) != 1 || result[0] != "T1" {
		t.Errorf("NeedsMeasuring = %v, want [T1]", result)
	}
}

func TestSuperviseNeedsMeasuringSkipsMeasured(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1"},
		{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc},
		{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc},
		{TS: "2026-09-12T02:00:01Z", Task: "T1", Kind: "owns_checked", Attempt: "r1", Tree: "t"},
	}

	result := NeedsMeasuring(events)
	if len(result) != 0 {
		t.Errorf("NeedsMeasuring = %v, want []", result)
	}
}

// TestSuperviseNeedsMeasuringPartialPassIsNotMeasured checks a validate pass
// that recorded a gate but never reached owns_checked (it was interrupted) is
// measured again (#298 review).
func TestSuperviseNeedsMeasuringPartialPassIsNotMeasured(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1"},
		{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc},
		{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc},
	}
	if got := NeedsMeasuring(events); len(got) != 1 || got[0] != "T1" {
		t.Errorf("NeedsMeasuring = %v, want [T1] (the pass never completed)", got)
	}
}

// TestSuperviseNeedsMeasuringUsesNewestFinish checks the finish cutoff is the
// newest finish by timestamp, not the last line: a reading after an older
// finish but before the newest does not count (#298 review).
func TestSuperviseNeedsMeasuringUsesNewestFinish(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1"},
		{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc},
		{TS: "2026-09-12T02:00:01Z", Task: "T1", Kind: "owns_checked", Attempt: "r1", Tree: "t"},
		{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc},
	}
	if got := NeedsMeasuring(events); len(got) != 1 || got[0] != "T1" {
		t.Errorf("NeedsMeasuring = %v, want [T1] (the newest finish is after the reading)", got)
	}
}

func TestSuperviseMeasuresFinishedTask(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() error = %v", err)
	}

	if len(result.Measured) != 1 {
		t.Fatalf("Measured len = %d, want 1", len(result.Measured))
	}
	if result.Measured[0].Task != "T1" {
		t.Errorf("Task = %q, want T1", result.Measured[0].Task)
	}
	if result.Measured[0].OK != true {
		t.Errorf("OK = %v, want true", result.Measured[0].OK)
	}
	if result.Measured[0].Error != "" {
		t.Errorf("Error = %q, want empty", result.Measured[0].Error)
	}

	// Verify that a validated event was added
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var foundValidated bool
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "validated" && e.Attempt == "r1" {
			foundValidated = true
			break
		}
	}
	if !foundValidated {
		t.Error("validated event not found in ledger")
	}
}

func TestSuperviseReportsFailingGauges(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 1"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() error = %v", err)
	}

	if len(result.Measured) != 1 {
		t.Fatalf("Measured len = %d, want 1", len(result.Measured))
	}
	if result.Measured[0].Task != "T1" {
		t.Errorf("Task = %q, want T1", result.Measured[0].Task)
	}
	if result.Measured[0].OK != false {
		t.Errorf("OK = %v, want false", result.Measured[0].OK)
	}
}

func TestSuperviseSecondPassMeasuresNothing(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 0 {
		t.Errorf("second pass Measured len = %d, want 0", len(result2.Measured))
	}
}

// TestSuperviseRemeasuresPassedUnitAfterOwnedEdit checks that supervise
// re-measures a unit inspected as passed after an owned file changes (#300).
func TestSuperviseRemeasuresPassedUnitAfterOwnedEdit(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	var readingTree string
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "owns_checked" {
			readingTree = e.Tree
			break
		}
	}
	if readingTree == "" {
		t.Fatal("owns_checked event with tree not found")
	}

	if err := AppendEvent(dir, Event{
		TS:      "2026-09-12T02:30:00Z",
		Task:    "T1",
		Kind:    "inspected",
		Attempt: "r1",
		Verdict: "pass",
		Session: "insp",
		Persona: "inspector",
		Tree:    readingTree,
	}); err != nil {
		t.Fatalf("AppendEvent(inspected) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 1 {
		t.Errorf("second pass Measured len = %d, want 1", len(result2.Measured))
	}
	if len(result2.Measured) > 0 && result2.Measured[0].Task != "T1" {
		t.Errorf("second pass Task = %q, want T1", result2.Measured[0].Task)
	}
}

// TestSuperviseIgnoresPassedUnitWhenUnchanged checks that supervise does not
// re-measure a passed unit when its owned files haven't changed.
func TestSuperviseIgnoresPassedUnitWhenUnchanged(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	var readingTree string
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "owns_checked" {
			readingTree = e.Tree
			break
		}
	}
	if readingTree == "" {
		t.Fatal("owns_checked event with tree not found")
	}

	if err := AppendEvent(dir, Event{
		TS:      "2026-09-12T02:30:00Z",
		Task:    "T1",
		Kind:    "inspected",
		Attempt: "r1",
		Verdict: "pass",
		Session: "insp",
		Persona: "inspector",
		Tree:    readingTree,
	}); err != nil {
		t.Fatalf("AppendEvent(inspected) error = %v", err)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 0 {
		t.Errorf("second pass Measured len = %d, want 0", len(result2.Measured))
	}
}

// TestSuperviseIgnoresEditOutsideOwns checks that supervise does not
// re-measure a passed unit when only files outside its owns changed.
func TestSuperviseIgnoresEditOutsideOwns(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	var readingTree string
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "owns_checked" {
			readingTree = e.Tree
			break
		}
	}
	if readingTree == "" {
		t.Fatal("owns_checked event with tree not found")
	}

	if err := AppendEvent(dir, Event{
		TS:      "2026-09-12T02:30:00Z",
		Task:    "T1",
		Kind:    "inspected",
		Attempt: "r1",
		Verdict: "pass",
		Session: "insp",
		Persona: "inspector",
		Tree:    readingTree,
	}); err != nil {
		t.Fatalf("AppendEvent(inspected) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatalf("write other.go: %v", err)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 0 {
		t.Errorf("second pass Measured len = %d, want 0", len(result2.Measured))
	}
}
