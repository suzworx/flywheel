package flywheel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnitSpend checks a unit's spend sums Cost over its own finished events
// only: every attempt, corrections included, never another task's (issue #459).
func TestUnitSpend(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Task: "T1", Kind: "finished", Attempt: "r1", Cost: 0.25},
		{Task: "T1", Kind: "dispatched", Attempt: "r2", Cost: 9},
		{Task: "T2", Kind: "finished", Attempt: "r1", Cost: 5},
		{Task: "T1", Kind: "finished", Attempt: "c1", Cost: 0.5},
	}
	if got := unitSpend(events, "T1"); got != 0.75 {
		t.Errorf("unitSpend(T1) = %v, want 0.75", got)
	}
	if got := unitSpend(events, "T3"); got != 0 {
		t.Errorf("unitSpend(T3) = %v, want 0", got)
	}
}

// spentTask returns setupTask's dir with a failed T1 attempt that spent cost
// and a sim config (clean.jsonl) capped at limits.unit_cost_usd capUSD.
func spentTask(t *testing.T, cost, capUSD float64) string {
	t.Helper()
	dir := setupTask(t)
	for _, e := range []Event{
		{TS: "2026-09-12T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-12T00:02:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Reason: "error", Cost: cost},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Limits.UnitCostUSD = capUSD
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	return dir
}

// TestRunUnitCostRefused checks a unit whose spend has reached its cap is
// refused at dispatch with the unit-cost rule (exit 6), appending only
// dispatch_refused (issue #651),
// and that the same unit below its cap dispatches (issue #459).
func TestRunUnitCostRefused(t *testing.T) {
	t.Parallel()
	dir := spentTask(t, 0.5, 0.5)
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1"})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "unit-cost" {
		t.Fatalf("Run() at the cap error = %v, want a unit-cost RuleRefusal", err)
	}
	for _, want := range []string{"$0.5000", "flywheel config set limits.unit_cost_usd <usd>", "split the unit"} {
		if !strings.Contains(rr.Fix, want) {
			t.Errorf("Fix = %q, want it to name %q", rr.Fix, want)
		}
	}
	wantRefusedAppended(t, dir, before, "T1", rr.Rule)

	below := spentTask(t, 0.5, 0.75)
	if _, err := Run(below, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() below the cap error = %v, want a dispatch", err)
	}
}

// TestRunUnitCostClaudeBudget checks a claude dispatch carries what the unit
// may still spend (cap minus recorded spend) and nothing when no cap is set
// (issue #459). Not parallel: commandHook and t.Setenv PATH, as in
// TestRunGeneralizesToNonSimAdapters.
func TestRunUnitCostClaudeBudget(t *testing.T) {
	for _, tc := range []struct{ capUSD, want float64 }{{2, 1.75}, {0, 0}} {
		dir := spentTask(t, 0.25, 0)
		cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
		cfg.Limits.UnitCostUSD = tc.capUSD
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		t.Setenv("PATH", t.TempDir())
		var got RunRequest
		commandHook = func(r RunRequest) { got = r }
		_, _ = Run(dir, RunOptions{Task: "T1"})
		commandHook = nil
		if got.Model == "" {
			t.Fatalf("cap %v: commandHook never fired", tc.capUSD)
		}
		if got.MaxBudgetUSD != tc.want {
			t.Errorf("cap %v: RunRequest.MaxBudgetUSD = %v, want %v", tc.capUSD, got.MaxBudgetUSD, tc.want)
		}
	}
}

// TestRunCappedStreaming checks a streaming worker whose per-step costs cross
// the unit cost cap is stopped at that step and finishes capped: the note
// names the cap and the spend, the written owned file is checkpointed, the
// capped signal is recorded, and the cost-cap hint (not the length hint) is
// printed (issue #459).
func TestRunCappedStreaming(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "costly.jsonl")
	lines := `{"type":"step_start","sessionID":"ses_uc","part":{"type":"step_start","step":1}}
{"type":"tool_use","sessionID":"ses_uc","part":{"type":"tool_use","tool":"write","state":{"input":{"filePath":"a.go"}}}}
{"type":"step_finish","sessionID":"ses_uc","part":{"type":"step_finish","reason":"tool-calls","tokens":{"input":1,"output":1},"cost":0.25}}
{"type":"step_finish","sessionID":"ses_uc","part":{"type":"step_finish","reason":"tool-calls","tokens":{"input":1,"output":1},"cost":0.5}}
{"type":"step_finish","sessionID":"ses_uc","part":{"type":"step_finish","reason":"stop","tokens":{"input":1,"output":1},"cost":0.5}}
`
	if err := os.WriteFile(fixture, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := simConfig(fixture)
	cfg.Limits.UnitCostUSD = 0.5
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Worktree: true, Progress: &out})
	if err != nil || res.Reason != "capped" {
		t.Fatalf("Run() = %+v, %v; want reason capped", res, err)
	}
	if res.Steps != 2 {
		t.Errorf("steps = %d, want 2 (stopped at the step that crossed the cap)", res.Steps)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fin Event
	signalled := false
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "finished" {
			fin = e
		}
		if e.Task == "T1" && e.Kind == "signal" && e.Signal == "capped" {
			signalled = true
		}
	}
	if fin.Reason != "capped" || !strings.Contains(fin.Note, "unit cost cap $0.5000 reached: $0.7500 spent on the unit") {
		t.Errorf("finished = %+v, want reason capped with the cap note", fin)
	}
	if fin.Cost != 0.75 {
		t.Errorf("finished cost = %v, want 0.75 (the steps streamed before the stop)", fin.Cost)
	}
	if fin.Checkpoint == "" {
		t.Errorf("finished = %+v, want a checkpoint", fin)
	}
	if !signalled {
		t.Error("no capped signal recorded")
	}
	if !strings.Contains(out.String(), "T1 r1 hint: the unit reached limits.unit_cost_usd; raise it or split the unit") {
		t.Errorf("progress missing the cost-cap hint; got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "hint: reason=length") {
		t.Errorf("progress printed the length hint for a cost cap; got:\n%s", out.String())
	}
}
