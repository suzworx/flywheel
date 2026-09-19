package flywheel

import (
	"slices"
	"testing"
)

func TestAuditWaveSelectsEveryCandidate(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T3", Kind: "inspected", Verdict: "pass"},
		{Task: "T3", Kind: "audited", Verdict: "conforms"},
		{Task: "T4", Kind: "inspected", Verdict: "fail"},
	}
	sel := SelectWave(events)
	want := []string{"T1", "T2"}
	if !slices.Equal(sel.Tasks, want) {
		t.Fatalf("got %v, want %v", sel.Tasks, want)
	}
	if sel.Mode != "wave" {
		t.Fatalf("got mode %q, want wave", sel.Mode)
	}
}

func TestAuditWaveEmpty(t *testing.T) {
	events := []Event{}
	sel := SelectWave(events)
	if sel.Tasks == nil {
		t.Fatalf("got nil, want non-nil empty slice")
	}
	if len(sel.Tasks) != 0 {
		t.Fatalf("got %d tasks, want 0", len(sel.Tasks))
	}
	if sel.Mode != "wave" {
		t.Fatalf("got mode %q, want wave", sel.Mode)
	}
}
