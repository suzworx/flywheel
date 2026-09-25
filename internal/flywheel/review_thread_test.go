package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewThreadRender checks the generated thread over two rounds: one
// finding closed, one re-reported, one dismissed, with the worker's answers
// and the open blocking findings; and that a hand-written file is never
// overwritten.
func TestReviewThreadRender(t *testing.T) {
	t.Parallel()
	majorC := ReviewFinding{Severity: "major", Category: "correctness", File: "c.go", Line: 7, Claim: "Leaks a handle", Scenario: "early return", Fix: "defer close"}
	events := []Event{{Task: "T1", Kind: "started", Attempt: "r1", Session: "w-1"}}
	events = append(events, roundEvents(1, blockerA, minorB, majorC)...)
	events = append(events,
		Event{Task: "T1", Kind: "finding_response", Attempt: "r2", Session: "w-1", Finding: "T1-r1-1", Verdict: "fixed", Note: "flushed at EOF"},
		Event{Task: "T1", Kind: "finding_response", Attempt: "r2", Session: "w-1", Finding: "T1-r1-3", Verdict: "disputed", Note: "closed by the caller"},
		Event{Task: "T1", Kind: "finding_response", Session: "lead-1", Finding: "T1-r1-3", Verdict: "disputed", Note: "dismissed: the caller closes it"},
	)
	events = append(events, roundEvents(2, blockerA)...)
	events[len(events)-1].Tree = "0123456789abcdef"
	got := string(RenderReviewThread(events, "T1"))
	for _, want := range []string{
		"# Review thread: T1\n" + reviewThreadMarker,
		"## Round 1 — correct, rev-1, m, tree -",
		"## Round 2 — correct, rev-1, m, tree 0123456",
		"- **T1-r1-1** [blocker/-] a.go:3 — Drops the last line",
		"  scenario: no trailing newline",
		"  fix hint: flush",
		"  status: re-reported in round 2 as T1-r2-1",
		"  - w-1 r2: fixed — flushed at EOF",
		"- **T1-r1-2** [minor/-] b.go:0 — typo\n  scenario: reads badly\n  fix hint: -\n  status: closed in round 2",
		"- **T1-r1-3** [major/correctness] c.go:7 — Leaks a handle",
		"  status: dismissed by lead-1: the caller closes it",
		"  - w-1 r2: disputed — closed by the caller",
		"- **T1-r2-1** [blocker/-] a.go:3 — Drops the last line\n  scenario: no trailing newline\n  fix hint: flush\n  status: open",
		"## Open blocking findings\n\nT1-r2-1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("thread lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lead-1 -: disputed") {
		t.Errorf("a dismissal is rendered as a worker answer:\n%s", got)
	}
	if none := string(RenderReviewThread(roundEvents(1, minorB), "T1")); !strings.Contains(none, "## Open blocking findings\n\nnone\n") {
		t.Errorf("no blocker: want none:\n%s", none)
	}

	dir := loopRepo(t)
	if err := AppendEvents(dir, roundEvents(1, blockerA)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".flywheel", "reviews", "T1.md")
	if err := WriteReviewThread(dir, "T1"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "T1-r1-1") {
		t.Fatalf("written thread = %q, %v", data, err)
	}
	if err := os.WriteFile(path, []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteReviewThread(dir, "T1"); err == nil {
		t.Error("a hand-written thread was overwritten")
	}
	if data, _ := os.ReadFile(path); string(data) != "my notes\n" {
		t.Errorf("hand-written thread changed: %q", data)
	}
}
