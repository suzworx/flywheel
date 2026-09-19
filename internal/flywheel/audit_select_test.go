package flywheel

import (
	"fmt"
	"slices"
	"testing"
)

func TestAuditSelectCandidates(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "inspected", Verdict: "fail"},
		{Task: "T3", Kind: "inspected", Verdict: "pass"},
		{Task: "T3", Kind: "audited", Verdict: "conforms"},
	}
	candidates := AuditCandidates(events)
	want := []string{"T1"}
	if !slices.Equal(candidates, want) {
		t.Fatalf("got %v, want %v", candidates, want)
	}
}

func TestAuditSelectCandidateReinspectedAfterAudit(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T1", Kind: "audited", Verdict: "conforms"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
	}
	candidates := AuditCandidates(events)
	want := []string{"T1"}
	if !slices.Equal(candidates, want) {
		t.Fatalf("got %v, want %v", candidates, want)
	}
}

func TestAuditSelectFirstArticlePerLine(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T3", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m2"},
		{Task: "T3", Kind: "inspected", Verdict: "pass"},
	}
	sel := SelectFirstArticles(events)
	want := []string{"T1", "T3"}
	if !slices.Equal(sel.Tasks, want) {
		t.Fatalf("got %v, want %v", sel.Tasks, want)
	}
	if sel.Mode != "first-article" {
		t.Fatalf("got mode %q, want first-article", sel.Mode)
	}
}

func TestAuditSelectFirstArticleSkipsAuditedLine(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "audited", Verdict: "conforms"},
		{Task: "T3", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m2"},
		{Task: "T3", Kind: "inspected", Verdict: "pass"},
	}
	sel := SelectFirstArticles(events)
	want := []string{"T3"}
	if !slices.Equal(sel.Tasks, want) {
		t.Fatalf("got %v, want %v", sel.Tasks, want)
	}
}

func TestAuditSelectRateAdjusts(t *testing.T) {
	tests := []struct {
		name string
		seed int64
		rate float64
		want float64
	}{
		{
			name: "no audits",
			rate: 0.2,
			want: 0.2,
		},
		{
			name: "two nonconformances in last 10",
			rate: 0.2,
			want: 0.6,
			seed: 1,
		},
		{
			name: "10 conforming audits",
			rate: 0.2,
			want: 0.1,
			seed: 2,
		},
		{
			name: "rate capped at 1",
			rate: 0.5,
			want: 1.0,
			seed: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []Event

			if tt.name == "no audits" {
				// No audits yet
			} else if tt.name == "two nonconformances in last 10" {
				// Two nonconformances in the last 10
				for i := 0; i < 8; i++ {
					events = append(events, Event{
						Task:    fmt.Sprintf("T%d", i),
						Kind:    "audited",
						Verdict: "conforms",
					})
				}
				events = append(events, Event{Task: "T8", Kind: "audited", Verdict: "nonconformance"})
				events = append(events, Event{Task: "T9", Kind: "audited", Verdict: "nonconformance"})
			} else if tt.name == "10 conforming audits" {
				// 10 conforming audits
				for i := 0; i < 10; i++ {
					events = append(events, Event{
						Task:    fmt.Sprintf("T%d", i),
						Kind:    "audited",
						Verdict: "conforms",
					})
				}
			} else if tt.name == "rate capped at 1" {
				// 3 nonconformances in the last 10
				for i := 0; i < 7; i++ {
					events = append(events, Event{
						Task:    fmt.Sprintf("T%d", i),
						Kind:    "audited",
						Verdict: "conforms",
					})
				}
				for i := 7; i < 10; i++ {
					events = append(events, Event{
						Task:    fmt.Sprintf("T%d", i),
						Kind:    "audited",
						Verdict: "nonconformance",
					})
				}
			}

			got := EffectiveAuditRate(events, tt.rate)
			if d := got - tt.want; d > 1e-9 || d < -1e-9 {
				t.Fatalf("got %f, want %f", got, tt.want)
			}
		})
	}
}

func TestAuditSelectSampleDeterministic(t *testing.T) {
	// Create 50 passed candidates
	var events []Event
	for i := 0; i < 50; i++ {
		task := fmt.Sprintf("T%d", i)
		events = append(events, Event{Task: task, Kind: "inspected", Verdict: "pass"})
	}

	// Test rate 1 selects all
	sel := SelectSample(events, 1.0, 42)
	if len(sel.Tasks) != 50 {
		t.Fatalf("rate 1: got %d tasks, want 50", len(sel.Tasks))
	}
	if sel.Mode != "sample" {
		t.Fatalf("got mode %q, want sample", sel.Mode)
	}

	// Test rate 0 selects none
	sel = SelectSample(events, 0.0, 42)
	if len(sel.Tasks) != 0 {
		t.Fatalf("rate 0: got %d tasks, want 0", len(sel.Tasks))
	}

	// Test rate 0.3 with seed 42 is deterministic
	sel1 := SelectSample(events, 0.3, 42)
	sel2 := SelectSample(events, 0.3, 42)
	if !slices.Equal(sel1.Tasks, sel2.Tasks) {
		t.Fatalf("rate 0.3 seed 42 not deterministic: got %v and %v", sel1.Tasks, sel2.Tasks)
	}

	// Test count is between 5 and 30 for rate 0.3
	count := len(sel1.Tasks)
	if count < 5 || count > 30 {
		t.Fatalf("rate 0.3 seed 42: got %d tasks, want between 5 and 30", count)
	}
}

// TestAuditSelectFirstArticleUsesPassingAttemptLine checks that a unit retried
// on another model belongs to the line that built its passing attempt, and that
// a pass at log position 0 still makes a candidate.
func TestAuditSelectFirstArticleUsesPassingAttemptLine(t *testing.T) {
	events := []Event{
		{Task: "T0", Kind: "inspected", Verdict: "pass"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T1", Kind: "inspected", Verdict: "fail"},
		{Task: "T1", Kind: "dispatched", Attempt: "r2", Adapter: "claude", Model: "m2"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
	}
	if got := AuditCandidates(events); len(got) != 3 || got[0] != "T0" {
		t.Errorf("candidates = %v, want [T0 T1 T2]", got)
	}
	got := SelectFirstArticles(events).Tasks
	if len(got) != 2 || got[0] != "T1" || got[1] != "T2" {
		t.Errorf("first articles = %v, want [T1 T2] (T1 is m2's, T2 is m1's)", got)
	}
}

// TestAuditSelectRetryKeepsAuditOnItsLine checks that an audit credits the
// line of the pass it audited: T1 passes on m1 and is audited, then retries
// and passes on m2. m1 has had its first article; m2 has not (#309 review).
func TestAuditSelectRetryKeepsAuditOnItsLine(t *testing.T) {
	events := []Event{
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T1", Kind: "audited", Verdict: "conforms"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T1", Kind: "dispatched", Attempt: "r2", Adapter: "claude", Model: "m2"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
	}
	got := SelectFirstArticles(events).Tasks
	if len(got) != 1 || got[0] != "T1" {
		t.Errorf("first articles = %v, want [T1] (m2's first article; m1 already audited)", got)
	}
}
