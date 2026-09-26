package flywheel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestExplainUnknownTask(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "test"},
	}
	_, err := Explain(events, "UNKNOWN")
	if err == nil {
		t.Fatal("expected error for unknown task")
	}
	if !strings.Contains(err.Error(), "no events for task") {
		t.Fatalf("expected 'no events for task' in error, got: %v", err)
	}
}

func TestExplainSummaryFields(t *testing.T) {
	t.Parallel()
	header := &BriefHeader{
		Gates:  []string{"gate1", "gate2"},
		SHA256: "abcdef1234567890",
	}
	events := []Event{
		{
			TS:      "2026-09-18T10:00:00Z",
			Task:    "T1",
			Kind:    "planned",
			Brief:   "my brief",
			Owns:    []string{"a.txt", "b.txt"},
			Needs:   []string{"pkg1", "pkg2"},
			Session: "lead-1",
			Model:   "m1",
			GoalID:  "G1",
			Header:  header,
		},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Steps: 10, Cost: 0.5, RC: intPtr(0)},
		{TS: "2026-09-18T10:03:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:04:00Z", Task: "T1", Kind: "finished", Attempt: "c1", Steps: 5, Cost: 0.25, RC: intPtr(0)},
		{TS: "2026-09-18T10:05:00Z", Task: "T1", Kind: "landed", Commit: "abc123", Tree: "def456xyz"},
	}

	x, err := Explain(events, "T1")
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}

	if x.Brief != "my brief" {
		t.Errorf("Brief: got %q, want %q", x.Brief, "my brief")
	}
	if len(x.Owns) != 2 || x.Owns[0] != "a.txt" {
		t.Errorf("Owns: got %v, want [a.txt b.txt]", x.Owns)
	}
	if x.Planner != "lead-1" {
		t.Errorf("Planner: got %q, want %q", x.Planner, "lead-1")
	}
	if x.PlannerModel != "m1" {
		t.Errorf("PlannerModel: got %q, want %q", x.PlannerModel, "m1")
	}
	if x.GoalID != "G1" {
		t.Errorf("GoalID: got %q, want %q", x.GoalID, "G1")
	}
	if len(x.Attempts) != 2 || x.Attempts[0] != "r1" || x.Attempts[1] != "c1" {
		t.Errorf("Attempts: got %v, want [r1 c1]", x.Attempts)
	}
	if x.Steps != 15 {
		t.Errorf("Steps: got %d, want 15", x.Steps)
	}
	if x.Cost != 0.75 {
		t.Errorf("Cost: got %.2f, want 0.75", x.Cost)
	}
	if x.Commit != "abc123" {
		t.Errorf("Commit: got %q, want %q", x.Commit, "abc123")
	}
	if x.Tree != "def456xyz" {
		t.Errorf("Tree: got %q, want %q", x.Tree, "def456xyz")
	}
	if len(x.Timeline) != 6 {
		t.Errorf("Timeline length: got %d, want 6", len(x.Timeline))
	}
}

func TestExplainIgnoresOtherTasks(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "task 1"},
		{TS: "2026-09-18T10:01:00Z", Task: "T2", Kind: "planned", Brief: "task 2"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:03:00Z", Task: "T2", Kind: "dispatched", Attempt: "r1"},
	}

	x, err := Explain(events, "T1")
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}

	if len(x.Timeline) != 2 {
		t.Errorf("Timeline length: got %d, want 2 (only T1 events)", len(x.Timeline))
	}
	for _, e := range x.Timeline {
		if !strings.Contains(e.Kind, "planned") && !strings.Contains(e.Kind, "dispatched") {
			t.Errorf("Unexpected event kind in timeline: %s", e.Kind)
		}
	}
}

// TestExplainRoute checks a routed dispatched event says how it was routed
// and an unrouted one does not (issue #474).
func TestExplainRoute(t *testing.T) {
	t.Parallel()
	e := Event{Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "b",
		Route: &RouteChoice{Model: "b", Pick: "exploit", Objective: "cost_per_accepted"}}
	if got, want := explainLine(e), "dispatched r1 to claude b routed exploit by cost_per_accepted"; got != want {
		t.Errorf("explainLine(routed) = %q, want %q", got, want)
	}
	e.Route = nil
	if got := explainLine(e); strings.Contains(got, "routed") {
		t.Errorf("explainLine(unrouted) = %q, want no routed", got)
	}
}

func TestExplainLineFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		e    Event
		want []string // substrings that must appear in the line
	}{
		{
			name: "finished with rc 0",
			e:    Event{Kind: "finished", Attempt: "r1", RC: intPtr(0), Reason: "stop", Steps: 10, Cost: 0.5, Wrote: []string{"a.txt", "b.txt"}},
			want: []string{"finished", "r1", "rc=0", "steps=10", "cost=$0.50", "wrote 2 files"},
		},
		{
			name: "finished with rc non-zero",
			e:    Event{Kind: "finished", Attempt: "r1", RC: intPtr(1), Reason: "error", Steps: 5, Cost: 0.25},
			want: []string{"finished", "r1", "rc=1", "steps=5", "cost=$0.25"},
		},
		{
			name: "validated pass",
			e:    Event{Kind: "validated", Gate: "go build", RC: intPtr(0), Tree: "abcdef1234567890", DurationMS: 100},
			want: []string{"gate go build: pass", "in 100ms", "tree abcdef12"},
		},
		{
			name: "validated fail",
			e:    Event{Kind: "validated", Gate: "go test", RC: intPtr(1), Tree: "xyz"},
			want: []string{"gate go test: fail rc=1", "tree xyz"},
		},
		{
			name: "validated inconclusive",
			e:    Event{Kind: "validated", Gate: "check", Reason: "inconclusive", Tree: "1234567890"},
			want: []string{"gate check: inconclusive", "tree 12345678"},
		},
		{
			name: "owns_checked ok",
			e:    Event{Kind: "owns_checked"},
			want: []string{"owns check: ok"},
		},
		{
			name: "owns_checked outside",
			e:    Event{Kind: "owns_checked", Outside: []string{"external.txt", "other.go"}},
			want: []string{"owns check: outside external.txt", "other.go"},
		},
		{
			name: "inspected pass",
			e:    Event{Kind: "inspected", Verdict: "pass", Session: "lead-1", Persona: "inspector"},
			want: []string{"inspected pass by lead-1", "(inspector)"},
		},
		{
			name: "signal",
			e:    Event{Kind: "signal", Signal: "no-plan", Attempt: "r1"},
			want: []string{"signal no-plan on r1"},
		},
		{
			name: "landed",
			e:    Event{Kind: "landed", Commit: "abc123def", Tree: "fedcba9876543210", Note: "merge PR #1"},
			want: []string{"landed abc123def", "tree fedcba98", "— merge PR #1"},
		},
		{
			name: "review_finding",
			e:    Event{Kind: "review_finding", Severity: "major", Finding: "T1-r2-1", Path: "a.go", LineNo: 7, Title: "error swallowed"},
			want: []string{"major finding T1-r2-1 at a.go:7: error swallowed"},
		},
		{
			name: "note",
			e:    Event{Kind: "note", Task: "t1", Attempt: "r1", Note: "result: merged"},
			want: []string{"note: result: merged"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := explainLine(tt.e)
			for _, want := range tt.want {
				if !strings.Contains(line, want) {
					t.Errorf("line %q missing %q", line, want)
				}
			}
		})
	}
}

func TestExplainRenderMarkdown(t *testing.T) {
	t.Parallel()
	x := Explanation{
		Task:         "T1",
		Status:       "landed",
		Brief:        "test task",
		BriefSHA256:  "abc123defgh",
		Owns:         []string{"a.txt"},
		Needs:        []string{"pkg1"},
		Planner:      "lead-1",
		PlannerModel: "m1",
		Attempts:     []string{"r1"},
		Steps:        10,
		Cost:         0.5,
		Commit:       "abc123",
		Tree:         "def456",
		Timeline: []ExplainEntry{
			{TS: "2026-09-18T10:00:00Z", Kind: "planned", Line: "planned brief test task"},
			{TS: "2026-09-18T10:01:00Z", Kind: "finished", Attempt: "r1", Line: "finished r1 rc=0"},
		},
	}

	var buf bytes.Buffer
	err := RenderExplanation(&buf, x)
	if err != nil {
		t.Fatalf("RenderExplanation failed: %v", err)
	}

	markdown := buf.String()
	checks := []string{
		"# T1 — ",
		"## Timeline",
		"- steps: ",
		"**finished**",
	}

	for _, check := range checks {
		if !strings.Contains(markdown, check) {
			t.Errorf("markdown missing %q\nMarkdown:\n%s", check, markdown)
		}
	}
}

func TestExplainJSON(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "test"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
	}

	x, err := Explain(events, "T1")
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}

	b, err := json.Marshal(x)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	var decoded Explanation
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if decoded.Task != "T1" {
		t.Errorf("decoded task: got %q, want %q", decoded.Task, "T1")
	}
	if len(decoded.Timeline) != 2 {
		t.Errorf("decoded timeline length: got %d, want 2", len(decoded.Timeline))
	}
}

// Helper to create an int pointer.
func intPtr(i int) *int {
	return &i
}

// TestExplainUsesDerivationOrder checks the summary follows the same order
// Derive replays, not file position: an amendment appended to the file first
// but stamped later is the current brief.
func TestExplainUsesDerivationOrder(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T12:00:00Z", Task: "T1", Kind: "amended", Brief: "late.txt", Note: "later"},
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "first.txt"},
		{TS: "2026-09-18T11:00:00Z", Task: "T1", Kind: "amended", Brief: "middle.txt", Note: "earlier"},
	}
	x, err := Explain(events, "T1")
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if x.Brief != "late.txt" {
		t.Errorf("Brief = %q, want late.txt (the chronologically latest amendment)", x.Brief)
	}
	if len(x.Timeline) != 3 || x.Timeline[0].Kind != "planned" || x.Timeline[2].TS != "2026-09-18T12:00:00Z" {
		t.Errorf("timeline = %+v, want chronological order", x.Timeline)
	}
}

// TestExplainSmallCostVisible checks a sub-cent cost is not rounded away: the
// line and the total show four decimals, like flywheel cost.
func TestExplainSmallCostVisible(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "finished", Attempt: "r1", RC: &rc, Reason: "stop", Steps: 3, Cost: 0.004},
	}
	x, err := Explain(events, "T1")
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if !strings.Contains(x.Timeline[1].Line, "cost=$0.0040") {
		t.Errorf("finished line = %q, want cost=$0.0040", x.Timeline[1].Line)
	}
	var b bytes.Buffer
	if err := RenderExplanation(&b, x); err != nil {
		t.Fatalf("RenderExplanation() error = %v", err)
	}
	if !strings.Contains(b.String(), "cost: $0.0040") {
		t.Errorf("summary lacks cost: $0.0040:\n%s", b.String())
	}
}
