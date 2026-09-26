package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// limitedUnit is task's planned, dispatched and rate-limited finished events
// on model, its reset at reset.
func limitedUnit(task, model, reason string, reset time.Time) []Event {
	return []Event{
		{Task: task, Kind: "planned", Brief: "brief.txt"},
		{Task: task, Kind: "dispatched", Attempt: "r1", Model: model},
		{Task: task, Kind: "finished", Attempt: "r1", Model: model, Reason: reason, ResetAt: reset.Format(time.RFC3339)},
	}
}

// resumeLimitedPass runs SuperviseWith --resume-limited at recoverNow, appending
// every started task to calls.
func resumeLimitedPass(t *testing.T, dir string, on bool, calls *[]string) SuperviseResult {
	t.Helper()
	res, err := SuperviseWith(dir, SuperviseOptions{ResumeLimited: on, Now: recoverNow, Session: "sup",
		Start: func(task string) error { *calls = append(*calls, task); return nil }})
	if err != nil {
		t.Fatalf("SuperviseWith: %v", err)
	}
	return res
}

// autoResumes returns the ledger's auto-resume recovered events.
func autoResumes(t *testing.T, dir string) []Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range events {
		if e.Kind == "recovered" && strings.HasPrefix(e.Note, "auto-resume ") {
			out = append(out, e)
		}
	}
	return out
}

// TestSuperviseResumeLimitedStarts: a rate-limited unit whose model's reset
// has passed is started once and recorded by one recovered event (issue #472).
func TestSuperviseResumeLimitedStarts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir, limitedUnit("T", "m", "rate-limited", recoverNow.Add(-time.Hour))...)
	var calls []string
	res := resumeLimitedPass(t, dir, true, &calls)
	if !slices.Equal(calls, []string{"T"}) {
		t.Errorf("Start calls = %q, want [T]", calls)
	}
	if len(res.Resumed) != 1 || !res.Resumed[0].Started || res.Resumed[0].Attempt != "r1" {
		t.Errorf("Resumed = %+v, want T r1 started", res.Resumed)
	}
	if ev := autoResumes(t, dir); len(ev) != 1 || !strings.HasPrefix(ev[0].Note, "auto-resume T r1") || ev[0].Session != "sup" || !slices.Equal(ev[0].Paths, []string{"T"}) {
		t.Errorf("auto-resume events = %+v, want one for T r1 by sup", ev)
	}
}

// TestSuperviseResumeLimitedOncePerFinish: a second pass before the child
// records anything starts nothing; a new rate-limited finish is resumed again.
func TestSuperviseResumeLimitedOncePerFinish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir, limitedUnit("T", "m", "rate-limited", recoverNow.Add(-time.Hour))...)
	var calls []string
	resumeLimitedPass(t, dir, true, &calls)
	if res := resumeLimitedPass(t, dir, true, &calls); len(calls) != 1 || len(res.Resumed) != 0 {
		t.Fatalf("second pass: calls = %q, Resumed = %+v; want no second start", calls, res.Resumed)
	}
	reset := recoverNow.Add(-time.Hour).Format(time.RFC3339)
	for _, e := range []Event{
		{TS: recoverNow.Add(-2 * time.Hour).Format(time.RFC3339), Task: "T", Kind: "dispatched", Attempt: "r2", Model: "m"},
		{TS: recoverNow.Add(-90 * time.Minute).Format(time.RFC3339), Task: "T", Kind: "finished", Attempt: "r2", Model: "m", Reason: "rate-limited", ResetAt: reset},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	resumeLimitedPass(t, dir, true, &calls)
	if !slices.Equal(calls, []string{"T", "T"}) || len(autoResumes(t, dir)) != 2 {
		t.Errorf("calls = %q, auto-resumes = %d; want a second start after the new finish", calls, len(autoResumes(t, dir)))
	}
}

// TestSuperviseResumeLimitedCap: once limits.rate_limit_retries auto-resumes
// since the unit was planned, a new rate-limited finish is reported, never
// started.
func TestSuperviseResumeLimitedCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	one := 1
	if err := WriteConfig(dir, Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "m"}}, Limits: Limits{RateLimitRetries: &one}}); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	reset := recoverNow.Add(-time.Hour).Format(time.RFC3339)
	recoverLedger(t, dir, append(limitedUnit("T", "m", "rate-limited", recoverNow.Add(-time.Hour)),
		Event{Kind: "recovered", Note: "auto-resume T r1 after rate limit (1/1)", Paths: []string{"T"}},
		Event{Task: "T", Kind: "dispatched", Attempt: "r2", Model: "m"},
		Event{Task: "T", Kind: "finished", Attempt: "r2", Model: "m", Reason: "rate-limited", ResetAt: reset})...)
	var calls []string
	res := resumeLimitedPass(t, dir, true, &calls)
	if len(calls) != 0 {
		t.Errorf("Start calls = %q, want none past the cap", calls)
	}
	if len(res.Resumed) != 1 || res.Resumed[0].Started || !strings.Contains(res.Resumed[0].Reason, "auto-resume cap 1 reached") {
		t.Errorf("Resumed = %+v, want T not started naming the cap", res.Resumed)
	}
	if n := len(autoResumes(t, dir)); n != 1 {
		t.Errorf("auto-resume events = %d, want the earlier one only", n)
	}
}

// TestSuperviseResumeLimitedSkips: without the flag nothing is started; with
// it a unit whose model is still paused (wait-reset) and one that ended
// abandoned-job are never started, only the rate-limited unit past its reset.
func TestSuperviseResumeLimitedSkips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var evs []Event
	evs = append(evs, limitedUnit("P", "m", "rate-limited", recoverNow.Add(time.Hour))...)
	evs = append(evs, limitedUnit("A", "n", "abandoned-job", recoverNow.Add(-time.Hour))...)
	evs = append(evs, limitedUnit("L", "n", "rate-limited", recoverNow.Add(-time.Hour))...)
	recoverLedger(t, dir, evs...)
	var calls []string
	if res := resumeLimitedPass(t, dir, false, &calls); len(calls) != 0 || len(res.Resumed) != 0 || len(autoResumes(t, dir)) != 0 {
		t.Fatalf("without --resume-limited: calls = %q, Resumed = %+v; want nothing", calls, res.Resumed)
	}
	resumeLimitedPass(t, dir, true, &calls)
	if !slices.Equal(calls, []string{"L"}) {
		t.Errorf("Start calls = %q, want only L (P paused, A abandoned-job)", calls)
	}
	for _, e := range autoResumes(t, dir) {
		if !slices.Equal(e.Paths, []string{"L"}) {
			t.Errorf("auto-resume event for %v, want L only", e.Paths)
		}
	}
}
