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
	t.Parallel()
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
// TestStatsReview checks the review block (issue #389): rounds, findings by
// severity and category, the median rounds to clean over units whose latest
// agent review is pass, the findings raised after every gate passed, and the
// dismissals.
func TestStatsReview(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	round := func(task string, n int, fs ...ReviewFinding) []Event {
		evs, _, _ := reviewEvents(task, "r1", n, "rev-1", "m", "claude", "", fs)
		return evs
	}
	rc0, rc1 := 0, 1
	start := func(task string, gate2 *int) []Event {
		return []Event{
			{Task: task, Kind: "planned", Brief: "b.txt"},
			{Task: task, Kind: "dispatched", Attempt: "r1"},
			{Task: task, Kind: "started", Attempt: "r1", Session: "w-" + task},
			{Task: task, Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc0, Command: "go vet ./..."},
			{Task: task, Kind: "validated", Attempt: "r1", Gate: "2", Tree: "t", RC: gate2, Command: "go test ./..."},
		}
	}
	majorC := ReviewFinding{Severity: "major", Category: "correctness", File: "c.go", Claim: "Leaks a handle", Scenario: "early return"}
	var events []Event
	// T1: gates green, two findings, then a clean round: 2 rounds to clean.
	events = append(events, start("T1", &rc0)...)
	events = append(events, round("T1", 1, blockerA, minorB)...)
	events = append(events, round("T1", 2)...)
	// T2: a gate red, one finding a lead dismisses, then clean: 2 rounds.
	events = append(events, start("T2", &rc1)...)
	events = append(events, round("T2", 1, majorC)...)
	events = append(events, Event{Task: "T2", Kind: "finding_response", Session: "lead-1", Finding: "T2-r1-1", Verdict: "disputed", Note: "dismissed: by design"})
	events = append(events, round("T2", 2)...)
	// T3: clean at once. T4: still open, not clean.
	events = append(events, start("T3", &rc0)...)
	events = append(events, round("T3", 1)...)
	events = append(events, start("T4", &rc0)...)
	events = append(events, round("T4", 1, majorC)...)
	if err := AppendEvents(dir, events); err != nil {
		t.Fatal(err)
	}
	rep, err := Stats(dir)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	rv := rep.Review
	if rv.Reviews != 6 || rv.Findings != 4 || rv.Dismissals != 1 {
		t.Errorf("reviews %d findings %d dismissals %d, want 6, 4, 1", rv.Reviews, rv.Findings, rv.Dismissals)
	}
	if rv.BySeverity["blocker"] != 1 || rv.BySeverity["major"] != 2 || rv.BySeverity["minor"] != 1 {
		t.Errorf("by severity = %v", rv.BySeverity)
	}
	if rv.ByCategory["correctness"] != 2 || rv.ByCategory["uncategorized"] != 2 {
		t.Errorf("by category = %v", rv.ByCategory)
	}
	if rv.CleanUnits != 3 || rv.MedianRoundsToClean != 2 {
		t.Errorf("clean units %d median %v, want 3 and 2", rv.CleanUnits, rv.MedianRoundsToClean)
	}
	// T1's two and T4's one were raised with every gate green; T2's with a red gate.
	if rv.CaughtAfterGates != 3 {
		t.Errorf("caught after gates = %d, want 3", rv.CaughtAfterGates)
	}
	if empty := reviewStats(nil); empty.Reviews != 0 || empty.MedianRoundsToClean != 0 {
		t.Errorf("empty log review = %+v", empty)
	}
}

func TestStatsEmptyLog(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestStatsBaselineRatio checks Stats calculates the baseline cost and ratio
// when config carries a baseline (issue #59).
func TestStatsBaselineRatio(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
		Baseline: &Baseline{
			Model:             "frontier-x",
			InputPerMTok:      5,
			OutputPerMTok:     25,
			CacheReadPerMTok:  0.5,
			CacheWritePerMTok: 6.25,
		},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	e := Event{
		TS:      "2026-01-01T00:00:00Z",
		Task:    "T1",
		Kind:    "finished",
		Attempt: "r1",
		Reason:  "stop",
		Cost:    0.05,
		Tokens:  &Tokens{Input: 1000000, Output: 100000},
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("append event: %v", err)
	}
	rep, err := Stats(dir)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if rep.Baseline == nil {
		t.Fatal("Baseline = nil, want non-nil")
	}
	if rep.Baseline.Model != "frontier-x" {
		t.Errorf("Baseline.Model = %q, want frontier-x", rep.Baseline.Model)
	}
	if rep.Baseline.Cost != 7.5 {
		t.Errorf("Baseline.Cost = %v, want 7.5 (5*1 + 25*0.3)", rep.Baseline.Cost)
	}
	if rep.Spend != 0.05 {
		t.Errorf("Spend = %v, want 0.05", rep.Spend)
	}
	wantRatio := round(0.05/7.5, 4)
	if rep.Baseline.Ratio != wantRatio {
		t.Errorf("Baseline.Ratio = %v, want %v (0.05 / 7.5)", rep.Baseline.Ratio, wantRatio)
	}
}

// TestStatsBaselineAbsent checks Stats sets Tokens and Spend even without a
// baseline in config, and Baseline is nil (issue #59).
func TestStatsBaselineAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultConfig()
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	e := Event{
		TS:      "2026-01-01T00:00:00Z",
		Task:    "T1",
		Kind:    "finished",
		Attempt: "r1",
		Reason:  "stop",
		Cost:    0.05,
		Tokens:  &Tokens{Input: 1000000, Output: 100000},
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("append event: %v", err)
	}
	rep, err := Stats(dir)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if rep.Tokens != (Tokens{Input: 1000000, Output: 100000}) {
		t.Errorf("Tokens = %v, want {1000000, 100000, 0, 0, 0}", rep.Tokens)
	}
	if rep.Spend != 0.05 {
		t.Errorf("Spend = %v, want 0.05", rep.Spend)
	}
	if rep.Baseline != nil {
		t.Errorf("Baseline = %v, want nil (not configured)", rep.Baseline)
	}
}

// TestStatsBaselineInvalidConfigErrors checks a config that fails validation
// is reported, not treated as having no baseline (#292 review).
func TestStatsBaselineInvalidConfigErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `{"version":1,"workers":[{"name":"default","adapter":"sim","model":"m"}],"baseline":{"model":"","input_per_mtok":1}}`
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Stats(dir); err == nil {
		t.Fatal("Stats() error = nil, want the invalid baseline reported")
	}
}
