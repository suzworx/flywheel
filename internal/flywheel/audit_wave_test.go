package flywheel

import (
	"errors"
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

// TestAuditWaveStaleCandidateSkipped checks that a unit audited after it was
// selected is refused, not audited twice, when the audit requires a candidate
// (#323 review).
func TestAuditWaveStaleCandidateSkipped(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	for _, e := range []Event{
		{TS: "2026-09-19T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-19T10:01:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-19T10:02:00Z", Task: "T1", Kind: "audited", Verdict: "conforms", Session: "aud-1"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	_, err := AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: "aud-2", RequireCandidate: true})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "audit" {
		t.Fatalf("AuditTask of an already-audited unit = %v, want an audit refusal", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "audited" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("audited events = %d, want 1 (no second audit)", n)
	}
}
