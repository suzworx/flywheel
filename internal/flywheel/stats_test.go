package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// statsFixture writes the stats gate's event log: a clean first-pass task
// (T1), a task that takes a correction (r1 then c1) and lands (T2), a capped
// finish that gets rejected (T3), and a landed task (T4).
func statsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-01-01T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-01-01T00:01:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Reason: "stop", Cost: 1.0},
		{TS: "2026-01-01T00:01:05Z", Task: "T1", Kind: "inspected", Attempt: "r1", Verdict: "pass"},

		{TS: "2026-01-01T01:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T01:00:00Z", Task: "T2", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-01-01T01:00:30Z", Task: "T2", Kind: "finished", Attempt: "r1", Reason: "stop", Cost: 1.0},
		{TS: "2026-01-01T01:00:35Z", Task: "T2", Kind: "inspected", Attempt: "r1", Verdict: "rework"},
		{TS: "2026-01-01T01:05:00Z", Task: "T2", Kind: "dispatched", Attempt: "c1"},
		{TS: "2026-01-01T01:06:30Z", Task: "T2", Kind: "finished", Attempt: "c1", Reason: "stop", Cost: 1.5},
		{TS: "2026-01-01T01:06:35Z", Task: "T2", Kind: "inspected", Attempt: "c1", Verdict: "pass"},
		{TS: "2026-01-01T01:07:00Z", Task: "T2", Kind: "landed"},

		{TS: "2026-01-01T02:00:00Z", Task: "T3", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T02:00:00Z", Task: "T3", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-01-01T02:00:45Z", Task: "T3", Kind: "finished", Attempt: "r1", Reason: "capped", Cost: 0.5},
		{TS: "2026-01-01T02:00:50Z", Task: "T3", Kind: "inspected", Attempt: "r1", Verdict: "scrap"},

		{TS: "2026-01-01T03:00:00Z", Task: "T4", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T03:00:00Z", Task: "T4", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-01-01T03:01:15Z", Task: "T4", Kind: "finished", Attempt: "r1", Reason: "stop", Cost: 2.0},
		{TS: "2026-01-01T03:01:20Z", Task: "T4", Kind: "inspected", Attempt: "r1", Verdict: "pass"},
		{TS: "2026-01-01T03:01:25Z", Task: "T4", Kind: "landed"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return dir
}

// TestStatsFixture asserts every field of StatsReport against the fixture's
// hand-computed numbers.
func TestStatsFixture(t *testing.T) {
	rep, err := Stats(statsFixture(t))
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	wantTasks := StatsTasks{Total: 4, Landed: 2, Passed: 1, Rejected: 1}
	if rep.Tasks != wantTasks {
		t.Errorf("Tasks = %#v, want %#v", rep.Tasks, wantTasks)
	}
	if rep.Tasks.LeadImplemented != 0 {
		t.Errorf("LeadImplemented = %d, want 0 (no lead-implemented landing in the fixture)", rep.Tasks.LeadImplemented)
	}
	if rep.FirstPassRate != 0.5 || rep.FirstPassCount != 2 || rep.FirstPassTotal != 4 {
		t.Errorf("first-pass = %.2f (%d/%d), want 0.50 (2/4)", rep.FirstPassRate, rep.FirstPassCount, rep.FirstPassTotal)
	}
	if rep.CorrectionsPerTask != 0.25 {
		t.Errorf("CorrectionsPerTask = %.2f, want 0.25", rep.CorrectionsPerTask)
	}
	if rep.FinishReasons["stop"] != 4 || rep.FinishReasons["capped"] != 1 || len(rep.FinishReasons) != 2 {
		t.Errorf("FinishReasons = %#v, want stop=4 capped=1", rep.FinishReasons)
	}
	if rep.UncleanPer100 != 20 {
		t.Errorf("UncleanPer100 = %.2f, want 20.00", rep.UncleanPer100)
	}
	if rep.MeanAttemptSeconds != 60 {
		t.Errorf("MeanAttemptSeconds = %d, want 60", rep.MeanAttemptSeconds)
	}
	if rep.CostPerLandedTask != 3.0 {
		t.Errorf("CostPerLandedTask = %.4f, want 3.0000", rep.CostPerLandedTask)
	}
}

// TestStatsEmptyLog checks an empty log gives zeroes for every field, with no
// division by zero.
func TestStatsEmptyLog(t *testing.T) {
	rep, err := Stats(t.TempDir())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if rep.Tasks != (StatsTasks{}) {
		t.Errorf("Tasks = %#v, want zero", rep.Tasks)
	}
	if rep.FirstPassRate != 0 || rep.FirstPassCount != 0 || rep.FirstPassTotal != 0 {
		t.Errorf("first-pass = %.2f (%d/%d), want 0.00 (0/0)", rep.FirstPassRate, rep.FirstPassCount, rep.FirstPassTotal)
	}
	if rep.CorrectionsPerTask != 0 {
		t.Errorf("CorrectionsPerTask = %.2f, want 0", rep.CorrectionsPerTask)
	}
	if len(rep.FinishReasons) != 0 {
		t.Errorf("FinishReasons = %#v, want none", rep.FinishReasons)
	}
	if rep.UncleanPer100 != 0 {
		t.Errorf("UncleanPer100 = %.2f, want 0", rep.UncleanPer100)
	}
	if rep.MeanAttemptSeconds != 0 {
		t.Errorf("MeanAttemptSeconds = %d, want 0", rep.MeanAttemptSeconds)
	}
	if rep.CostPerLandedTask != 0 {
		t.Errorf("CostPerLandedTask = %.4f, want 0", rep.CostPerLandedTask)
	}
}

// TestStatsLeadImplemented checks Stats counts only landed tasks whose landing
// carried the lead-implemented flag, and that the other figures are unaffected.
func TestStatsLeadImplemented(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-01-01T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T00:00:10Z", Task: "T1", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-01-01T00:00:20Z", Task: "T1", Kind: "landed", Commit: "abc1234"},

		{TS: "2026-01-01T01:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T01:00:10Z", Task: "T2", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-01-01T01:00:20Z", Task: "T2", Kind: "landed", Commit: "def5678", Note: "lead-implemented: script fix", LeadImplemented: true},

		{TS: "2026-01-01T02:00:00Z", Task: "T3", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-01-01T02:00:10Z", Task: "T3", Kind: "inspected", Verdict: "pass"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	rep, err := Stats(dir)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	want := StatsTasks{Total: 3, Landed: 2, LeadImplemented: 1, Passed: 1}
	if rep.Tasks != want {
		t.Errorf("Tasks = %#v, want %#v", rep.Tasks, want)
	}
	if rep.FirstPassRate != 1 || rep.FirstPassCount != 3 || rep.FirstPassTotal != 3 {
		t.Errorf("first-pass = %.2f (%d/%d), want 1.00 (3/3)", rep.FirstPassRate, rep.FirstPassCount, rep.FirstPassTotal)
	}
}

// TestStatsUnreadableLog checks Stats fails when the log cannot be read.
func TestStatsUnreadableLog(t *testing.T) {
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dot, "events.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir events.jsonl: %v", err)
	}
	if _, err := Stats(dir); err == nil {
		t.Error("Stats() on an unreadable log succeeded, want error")
	}
}
