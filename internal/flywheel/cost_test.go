package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// costFixture writes the cost gate's event log: two finished tasks with
// models m1 and m2.
func costFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "t1", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: "2026-09-14T10:05:00Z", Task: "t1", Kind: "finished", Attempt: "r1", Tokens: &Tokens{Input: 100, Output: 50, Reasoning: 10, CacheRead: 800, CacheWrite: 5}, Cost: 0.004},
		{TS: "2026-09-14T10:00:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "t2", Kind: "dispatched", Attempt: "r1", Model: "m2"},
		{TS: "2026-09-14T10:05:00Z", Task: "t2", Kind: "finished", Attempt: "r1", Tokens: &Tokens{Input: 50, Output: 10, Reasoning: 2, CacheRead: 100}, Cost: 0.001},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return dir
}

// costRow looks up row by id in a sorted row list.
func costRow(t *testing.T, rows []CostRow, id string) CostRow {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no cost row %q in %#v", id, rows)
	return CostRow{}
}

// TestCostSumsFixture checks the fixture sums per task, per model and in
// total, with ids sorted.
func TestCostSumsFixture(t *testing.T) {
	t.Parallel()
	rep, err := Cost(costFixture(t))
	if err != nil {
		t.Fatalf("Cost() error = %v", err)
	}
	wantTasks := []string{"t1", "t2"}
	for i, want := range wantTasks {
		if rep.Tasks[i].ID != want {
			t.Fatalf("Tasks[%d].ID = %q, want %q", i, rep.Tasks[i].ID, want)
		}
	}
	t1 := costRow(t, rep.Tasks, "t1")
	if t1.Count() != 965 || t1.Cost != 0.004 {
		t.Errorf("t1 row = %d tokens, $%.4f; want 965, $0.0040", t1.Count(), t1.Cost)
	}
	t2 := costRow(t, rep.Tasks, "t2")
	if t2.Count() != 162 || t2.Cost != 0.001 {
		t.Errorf("t2 row = %d tokens, $%.4f; want 162, $0.0010", t2.Count(), t2.Cost)
	}
	if len(rep.Tasks) != 2 {
		t.Errorf("Tasks = %#v, want 2 rows", rep.Tasks)
	}
	wantModels := []string{"m1", "m2"}
	for i, want := range wantModels {
		if rep.Models[i].ID != want {
			t.Fatalf("Models[%d].ID = %q, want %q", i, rep.Models[i].ID, want)
		}
	}
	m1 := costRow(t, rep.Models, "m1")
	if m1.Count() != 965 || m1.Cost != 0.004 {
		t.Errorf("m1 row = %d tokens, $%.4f; want 965, $0.0040", m1.Count(), m1.Cost)
	}
	m2 := costRow(t, rep.Models, "m2")
	if m2.Count() != 162 || m2.Cost != 0.001 {
		t.Errorf("m2 row = %d tokens, $%.4f; want 162, $0.0010", m2.Count(), m2.Cost)
	}
	if len(rep.Models) != 2 {
		t.Errorf("Models = %#v, want 2 rows", rep.Models)
	}
	if rep.Total.Count() != 1127 || rep.Total.Cost != 0.005 {
		t.Errorf("Total = %d tokens, $%.4f; want 1127, $0.0050", rep.Total.Count(), rep.Total.Cost)
	}
}

// TestCostUnknownModel checks a finished task without a dispatched event
// lands under the model "unknown".
func TestCostUnknownModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, e := range []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:05:00Z", Task: "t1", Kind: "finished", Attempt: "r1", Tokens: &Tokens{Input: 10, Output: 5}, Cost: 0.0005},
		{TS: "2026-09-14T10:00:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "t2", Kind: "dispatched", Attempt: "r1", Model: "m2"},
		{TS: "2026-09-14T10:05:00Z", Task: "t2", Kind: "finished", Attempt: "r1", Tokens: &Tokens{Input: 3}, Cost: 0.0001},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	rep, err := Cost(dir)
	if err != nil {
		t.Fatalf("Cost() error = %v", err)
	}
	if len(rep.Models) != 2 || rep.Models[0].ID != "m2" || rep.Models[1].ID != "unknown" {
		t.Fatalf("Models = %#v, want [m2 unknown]", rep.Models)
	}
	u := costRow(t, rep.Models, "unknown")
	if u.Count() != 15 || u.Cost != 0.0005 {
		t.Errorf("unknown row = %d tokens, $%.4f; want 15, $0.0005", u.Count(), u.Cost)
	}
	if rep.Total.Count() != 18 {
		t.Errorf("Total tokens = %d, want 18", rep.Total.Count())
	}
}

// TestCostEmptyLog checks an empty log prints zeros, not an error.
func TestCostEmptyLog(t *testing.T) {
	t.Parallel()
	rep, err := Cost(t.TempDir())
	if err != nil {
		t.Fatalf("Cost() error = %v", err)
	}
	if len(rep.Tasks) != 0 || len(rep.Models) != 0 {
		t.Errorf("empty log rows = %#v, want none", rep)
	}
	if rep.Total.Count() != 0 || rep.Total.Cost != 0 {
		t.Errorf("empty log total = %d tokens, $%.4f; want 0, $0.0000", rep.Total.Count(), rep.Total.Cost)
	}
}

// TestCostJSONKeys checks --json output carries the tasks, models and total
// keys and the model entries appear.
func TestCostJSONKeys(t *testing.T) {
	t.Parallel()
	rep, err := Cost(costFixture(t))
	if err != nil {
		t.Fatalf("Cost() error = %v", err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	s := string(b)
	for _, key := range []string{`"tasks"`, `"models"`, `"total"`, `"m1"`, `"m2"`, `"t1"`, `"t2"`} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON missing %s: %s", key, s)
		}
	}
}

// TestCostUnreadableLog checks Cost fails when the log cannot be read.
func TestCostUnreadableLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dot, "events.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir events.jsonl: %v", err)
	}
	if _, err := Cost(dir); err == nil {
		t.Error("Cost() on an unreadable log succeeded, want error")
	}
}

// TestCostPerAttemptModel checks a finished event is charged to its own
// attempt's dispatched model even when a later attempt's dispatched line
// precedes it in the log, and the fallbacks: no attempt uses the task's latest
// preceding model, no dispatched event is unknown (issue #473).
func TestCostPerAttemptModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T", Kind: "dispatched", Attempt: "r1", Model: "A"},
		{TS: "2026-09-14T10:02:00Z", Task: "T", Kind: "dispatched", Attempt: "r2", Model: "B"},
		{TS: "2026-09-14T10:01:00Z", Task: "T", Kind: "finished", Attempt: "r1", Reason: "stop", Cost: 0.5},
		{TS: "2026-09-14T10:03:00Z", Task: "T", Kind: "finished", Attempt: "r2", Reason: "stop", Cost: 0.25},
		{TS: "2026-09-14T10:00:00Z", Task: "U", Kind: "dispatched", Attempt: "r1", Model: "C"},
		{TS: "2026-09-14T10:01:00Z", Task: "U", Kind: "finished", Reason: "stop", Cost: 0.125},
		{TS: "2026-09-14T10:01:00Z", Task: "V", Kind: "finished", Attempt: "r1", Reason: "stop", Cost: 1},
	}
	if err := AppendEvents(dir, events); err != nil {
		t.Fatalf("AppendEvents() error = %v", err)
	}
	rep, err := Cost(dir)
	if err != nil {
		t.Fatalf("Cost() error = %v", err)
	}
	for id, want := range map[string]float64{"A": 0.5, "B": 0.25, "C": 0.125, "unknown": 1} {
		if got := costRow(t, rep.Models, id).Cost; got != want {
			t.Errorf("model %s cost = %v, want %v", id, got, want)
		}
	}
	if len(rep.Models) != 4 {
		t.Errorf("models = %#v, want exactly A, B, C and unknown", rep.Models)
	}
}
