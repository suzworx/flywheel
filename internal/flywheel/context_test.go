package flywheel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestContextEmpty(t *testing.T) {
	events := []Event{}
	cfg := Config{}
	pack := BuildContext(events, cfg, 5)

	if pack.Goals == nil {
		t.Errorf("Goals = nil, want non-nil slice")
	}
	if len(pack.Goals) != 0 {
		t.Errorf("len(Goals) = %d, want 0", len(pack.Goals))
	}

	if pack.InFlight == nil {
		t.Errorf("InFlight = nil, want non-nil slice")
	}
	if len(pack.InFlight) != 0 {
		t.Errorf("len(InFlight) = %d, want 0", len(pack.InFlight))
	}

	if pack.Blocked == nil {
		t.Errorf("Blocked = nil, want non-nil slice")
	}
	if len(pack.Blocked) != 0 {
		t.Errorf("len(Blocked) = %d, want 0", len(pack.Blocked))
	}

	if pack.Ready == nil {
		t.Errorf("Ready = nil, want non-nil slice")
	}
	if len(pack.Ready) != 0 {
		t.Errorf("len(Ready) = %d, want 0", len(pack.Ready))
	}

	if pack.Unjudged == nil {
		t.Errorf("Unjudged = nil, want non-nil slice")
	}
	if len(pack.Unjudged) != 0 {
		t.Errorf("len(Unjudged) = %d, want 0", len(pack.Unjudged))
	}

	if pack.Learnings == nil {
		t.Errorf("Learnings = nil, want non-nil slice")
	}
	if len(pack.Learnings) != 0 {
		t.Errorf("len(Learnings) = %d, want 0", len(pack.Learnings))
	}
}

func TestContextActiveGoalsOnly(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Kind: "goal", Goal: &GoalSpec{ID: "G1", Title: "Active goal", Status: "active"}},
		{TS: "2026-09-18T10:00:01Z", Kind: "goal", Goal: &GoalSpec{ID: "G2", Title: "Met goal", Status: "met"}},
	}
	cfg := Config{}
	pack := BuildContext(events, cfg, 5)

	if len(pack.Goals) != 1 {
		t.Fatalf("len(Goals) = %d, want 1", len(pack.Goals))
	}
	if pack.Goals[0].ID != "G1" {
		t.Errorf("Goals[0].ID = %q, want G1", pack.Goals[0].ID)
	}
	if pack.Goals[0].Status != "active" {
		t.Errorf("Goals[0].Status = %q, want active", pack.Goals[0].Status)
	}
}

func TestContextInFlightAndUnjudged(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:02Z", Task: "T2", Kind: "planned"},
		{TS: "2026-09-18T10:00:03Z", Task: "T2", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:04Z", Task: "T2", Kind: "finished", Attempt: "r1"},
	}
	cfg := Config{}
	pack := BuildContext(events, cfg, 5)

	if len(pack.InFlight) != 1 {
		t.Fatalf("len(InFlight) = %d, want 1", len(pack.InFlight))
	}
	if pack.InFlight[0].ID != "T1" {
		t.Errorf("InFlight[0].ID = %q, want T1", pack.InFlight[0].ID)
	}
	if pack.InFlight[0].Status != "dispatched" {
		t.Errorf("InFlight[0].Status = %q, want dispatched", pack.InFlight[0].Status)
	}

	if len(pack.Unjudged) != 1 {
		t.Fatalf("len(Unjudged) = %d, want 1", len(pack.Unjudged))
	}
	if pack.Unjudged[0].Task != "T2" {
		t.Errorf("Unjudged[0].Task = %q, want T2", pack.Unjudged[0].Task)
	}
	if pack.Unjudged[0].Kind != "uninspected" {
		t.Errorf("Unjudged[0].Kind = %q, want uninspected", pack.Unjudged[0].Kind)
	}
}

func TestContextRecentLearningsCapped(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "learning", Severity: "P1", Title: "L1", Observed: "o1", Evidence: "e1", Ask: "a1"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "learning", Severity: "P2", Title: "L2", Observed: "o2", Evidence: "e2", Ask: "a2"},
		{TS: "2026-09-18T10:00:02Z", Task: "T1", Kind: "dismissed", ID: "L-02"},
		{TS: "2026-09-18T10:00:03Z", Task: "T1", Kind: "learning", Severity: "P3", Title: "L3", Observed: "o3", Evidence: "e3", Ask: "a3"},
		{TS: "2026-09-18T10:00:04Z", Task: "T1", Kind: "learning", Severity: "P4", Title: "L4", Observed: "o4", Evidence: "e4", Ask: "a4"},
	}
	cfg := Config{}
	pack := BuildContext(events, cfg, 2)

	if len(pack.Learnings) != 2 {
		t.Fatalf("len(Learnings) = %d, want 2", len(pack.Learnings))
	}
	if pack.Learnings[0].ID != "L-03" {
		t.Errorf("Learnings[0].ID = %q, want L-03", pack.Learnings[0].ID)
	}
	if pack.Learnings[1].ID != "L-04" {
		t.Errorf("Learnings[1].ID = %q, want L-04", pack.Learnings[1].ID)
	}
	if pack.Learnings[0].Title != "L3" {
		t.Errorf("Learnings[0].Title = %q, want L3", pack.Learnings[0].Title)
	}
	if pack.Learnings[1].Title != "L4" {
		t.Errorf("Learnings[1].Title = %q, want L4", pack.Learnings[1].Title)
	}
}

func TestContextRenderMarkdown(t *testing.T) {
	events := []Event{}
	cfg := Config{}
	pack := BuildContext(events, cfg, 5)

	var buf bytes.Buffer
	if err := RenderContext(&buf, pack); err != nil {
		t.Fatalf("RenderContext() error = %v", err)
	}
	output := buf.String()

	if !strings.Contains(output, "# flywheel context") {
		t.Errorf("output does not contain '# flywheel context'")
	}
	if !strings.Contains(output, "## In flight") {
		t.Errorf("output does not contain '## In flight'")
	}
	if !strings.Contains(output, "## Needs a verdict or triage") {
		t.Errorf("output does not contain '## Needs a verdict or triage'")
	}
	if !strings.Contains(output, "- none") {
		t.Errorf("output does not contain '- none' for empty sections")
	}
}

// TestContextJSON verifies the ContextPack can be marshaled to JSON.
func TestContextJSON(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Kind: "goal", Goal: &GoalSpec{ID: "G1", Title: "Test goal", Status: "active"}},
	}
	cfg := Config{}
	pack := BuildContext(events, cfg, 5)

	b, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}

	if !strings.Contains(string(b), "\"goals\"") {
		t.Errorf("JSON does not contain 'goals' key")
	}
	if !strings.Contains(string(b), "\"in_flight\"") {
		t.Errorf("JSON does not contain 'in_flight' key")
	}
	if !strings.Contains(string(b), "G1") {
		t.Errorf("JSON does not contain goal ID G1")
	}
}
