package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roundEvents is one agent review round of T1 by session rev-1.
func roundEvents(round int, findings ...ReviewFinding) []Event {
	evs, _, _ := reviewEvents("T1", "r1", round, "rev-1", "m", "claude", "", findings)
	return evs
}

var (
	blockerA = ReviewFinding{Severity: "blocker", File: "a.go", Line: 3, Claim: "Drops the last line", Scenario: "no trailing newline", Fix: "flush"}
	minorB   = ReviewFinding{Severity: "minor", File: "b.go", Claim: "typo", Scenario: "reads badly"}
)

// openIDs lists the ids of OpenFindings(events, "T1").
func openIDs(events []Event) string {
	var ids []string
	for _, e := range OpenFindings(events, "T1") {
		ids = append(ids, e.Finding)
	}
	return strings.Join(ids, ",")
}

// TestOpenFindings checks when a finding opens and closes (issue #389).
func TestOpenFindings(t *testing.T) {
	worker := Event{Task: "T1", Kind: "started", Attempt: "r1", Session: "w-1"}
	r1 := append([]Event{worker}, roundEvents(1, blockerA, minorB)...)
	if got := openIDs(r1); got != "T1-r1-1,T1-r1-2" {
		t.Errorf("raised: open = %q", got)
	}
	fixed := append(append([]Event(nil), r1...), Event{Task: "T1", Kind: "finding_response", Session: "w-1", Finding: "T1-r1-1", Verdict: "fixed", Note: "done"})
	if got := openIDs(fixed); got != "T1-r1-1,T1-r1-2" {
		t.Errorf("worker fixed, not re-reviewed: open = %q, want both still open", got)
	}
	silent := append(append([]Event(nil), fixed...), roundEvents(2)...)
	if got := openIDs(silent); got != "" {
		t.Errorf("next round silent: open = %q, want none", got)
	}
	again := blockerA
	again.Claim, again.Line = "  drops the LAST   line ", 4
	reported := append(append([]Event(nil), fixed...), roundEvents(2, again)...)
	if got := openIDs(reported); got != "T1-r2-1" {
		t.Errorf("re-reported: open = %q, want T1-r2-1", got)
	}
	workerDismiss := append(append([]Event(nil), reported...), Event{Task: "T1", Kind: "finding_response", Session: "w-1", Finding: "T1-r2-1", Verdict: "disputed", Note: "dismissed: not real"})
	if got := openIDs(workerDismiss); got != "T1-r2-1" {
		t.Errorf("worker 'dismissed:' closed a finding: open = %q", got)
	}
	lead := append(append([]Event(nil), reported...), Event{Task: "T1", Kind: "finding_response", Session: "lead-1", Finding: "T1-r2-1", Verdict: "disputed", Note: "dismissed: by design"})
	if got := openIDs(lead); got != "" {
		t.Errorf("lead dismissal: open = %q, want none", got)
	}
	if got := openIDs(append(lead, roundEvents(3, blockerA)...)); got != "" {
		t.Errorf("dismissed claim re-reported: open = %q, want none", got)
	}
}

// loopRepo is a flywheel dir with T1 planned from brief.txt and started by
// worker session w-1.
func loopRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: a.go, b.go\nneeds: none\ngate: go test ./...\ngate: go vet ./...\n\n# TASK\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvents(dir, []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "started", Attempt: "r1", Session: "w-1"},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFindingsDelta checks the delta: header, blocking findings only, the
// answer contract; no blocking finding writes nothing.
func TestFindingsDelta(t *testing.T) {
	dir := loopRepo(t)
	if err := AppendEvents(dir, roundEvents(1, minorB)); err != nil {
		t.Fatal(err)
	}
	events, _ := ReadEvents(dir)
	if path, n, err := FindingsDelta(dir, events, "T1"); err != nil || n != 0 || path != "" {
		t.Fatalf("minor only: FindingsDelta = %q, %d, %v; want no file", path, n, err)
	}
	if err := AppendEvents(dir, roundEvents(2, blockerA, minorB)); err != nil {
		t.Fatal(err)
	}
	events, _ = ReadEvents(dir)
	path, n, err := FindingsDelta(dir, events, "T1")
	if err != nil || n != 1 || filepath.ToSlash(path) != ".flywheel/briefs/T1.review-2.txt" {
		t.Fatalf("FindingsDelta = %q, %d, %v", path, n, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"owns: a.go, b.go\n", "needs: none\n", "gate: go test ./...\n", "gate: go vet ./...\n",
		"# TASK: fix the review findings", "FINDING T1-r2-1 [blocker] a.go:3 — Drops the last line\n",
		"scenario: no trailing newline\n", "fix hint: flush\n", "`FINDING <id>: fixed <evidence>`"} {
		if !strings.Contains(text, want) {
			t.Errorf("delta lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "T1-r2-2") {
		t.Errorf("delta carries the minor finding:\n%s", text)
	}
}

// TestParseFindingResponses checks the answer lines of a worker's report.
func TestParseFindingResponses(t *testing.T) {
	report := "Done.\r\n- FINDING T1-r1-1: Fixed added the flush; go test exit=0\r\n" +
		"* `FINDING T1-r1-2: DISPUTED the claim misreads the loop`\n" +
		"FINDING T1-r9-9: fixed not asked\nFINDING T1-r1-3 fixed no colon\n"
	resp, missing := ParseFindingResponses(report, []string{"T1-r1-1", "T1-r1-2", "T1-r1-3", "T1-r1-4"})
	if e := resp["T1-r1-1"]; e.Kind != "finding_response" || e.Verdict != "fixed" || e.Note != "added the flush; go test exit=0" {
		t.Errorf("T1-r1-1 = %+v", e)
	}
	if e := resp["T1-r1-2"]; e.Verdict != "disputed" || e.Note != "the claim misreads the loop" {
		t.Errorf("T1-r1-2 = %+v", e)
	}
	if _, ok := resp["T1-r9-9"]; ok || len(resp) != 2 {
		t.Errorf("resp = %+v, want only the two asked and well-formed ids", resp)
	}
	if strings.Join(missing, ",") != "T1-r1-3,T1-r1-4" {
		t.Errorf("missing = %v", missing)
	}
}

// loopFakes returns a review fake that records the findings plan[round] and
// a correction fake that writes report (answers) for attempt c<k> by w-1.
func loopFakes(t *testing.T, dir string, plan map[int][]ReviewFinding, report string) (func(int) (ReviewAgentResult, error), func(string) (Result, error), *[]string) {
	var deltas []string
	review := func(round int) (ReviewAgentResult, error) {
		return ReviewAgentResult{Round: round}, AppendEvents(dir, roundEvents(round, plan[round]...))
	}
	correct := func(delta string) (Result, error) {
		deltas = append(deltas, delta)
		attempt := "c" + string(rune('0'+len(deltas)))
		if report != "" {
			if err := os.WriteFile(filepath.Join(dir, ".flywheel", "runs", "T1."+attempt+".report.md"), []byte(report), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return Result{Attempt: attempt, Session: "w-1"}, nil
	}
	return review, correct, &deltas
}

// responses lists T1's finding_response events.
func responses(t *testing.T, dir string) []Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == "finding_response" {
			out = append(out, e)
		}
	}
	return out
}

// TestReviewLoop drives the loop with fakes: fixed then clean passes; a
// worker that never answers gets missing responses; exhausted rounds return
// the open findings.
func TestReviewLoop(t *testing.T) {
	dir := loopRepo(t)
	review, correct, deltas := loopFakes(t, dir, map[int][]ReviewFinding{1: {blockerA, minorB}}, "FINDING T1-r1-1: fixed flushed; go test exit=0\n")
	res, err := ReviewLoop(dir, "T1", ReviewLoopOptions{Review: review, Correct: correct})
	if err != nil || res.Verdict != "pass" || res.Reviews != 2 || res.Corrections != 1 || len(res.Open) != 0 {
		t.Fatalf("fix loop = %+v, %v", res, err)
	}
	if len(*deltas) != 1 || !strings.HasSuffix(filepath.ToSlash((*deltas)[0]), "T1.review-1.txt") {
		t.Errorf("deltas = %v", *deltas)
	}
	if r := responses(t, dir); len(r) != 1 || r[0].Finding != "T1-r1-1" || r[0].Verdict != "fixed" || r[0].Session != "w-1" || r[0].Attempt != "c1" {
		t.Errorf("responses = %+v", r)
	}

	dir = loopRepo(t)
	always := map[int][]ReviewFinding{1: {blockerA}, 2: {blockerA}, 3: {blockerA}}
	review, correct, deltas = loopFakes(t, dir, always, "")
	res, err = ReviewLoop(dir, "T1", ReviewLoopOptions{Rounds: 3, Review: review, Correct: correct})
	if err != nil || res.Verdict != "open" || res.Reviews != 3 || res.Corrections != 2 {
		t.Fatalf("silent worker loop = %+v, %v", res, err)
	}
	if len(res.Open) != 1 || res.Open[0].Finding != "T1-r3-1" || strings.Join(res.Missing, ",") != "T1-r1-1,T1-r2-1" {
		t.Errorf("open = %+v, missing = %v", res.Open, res.Missing)
	}
	r := responses(t, dir)
	if len(r) != 2 || r[0].Verdict != "disputed" || r[0].Note != "missing: the worker gave no answer" {
		t.Errorf("missing responses = %+v", r)
	}
	if len(*deltas) != 2 {
		t.Errorf("deltas = %v, want 2", *deltas)
	}
}

// TestReviewLoopDismiss checks a lead's dismissal: a worker session is
// refused T4, an unknown id and an empty note are refused, and the lead's
// dismissal closes the finding.
func TestReviewLoopDismiss(t *testing.T) {
	dir := loopRepo(t)
	if err := AppendEvents(dir, roundEvents(1, blockerA)); err != nil {
		t.Fatal(err)
	}
	if err := DismissFinding(dir, "T1", "T1-r1-1", "w-1", "not real"); !IsRuleRefusal(err) {
		t.Errorf("worker dismissal = %v, want a T4 refusal", err)
	}
	if err := DismissFinding(dir, "T1", "T1-r7-1", "lead-1", "x"); err == nil {
		t.Error("unknown id dismissed")
	}
	if err := DismissFinding(dir, "T1", "T1-r1-1", "lead-1", " "); err == nil {
		t.Error("dismissal without a note accepted")
	}
	if err := DismissFinding(dir, "T1", "T1-r1-1", "lead-1", "by design"); err != nil {
		t.Fatalf("lead dismissal: %v", err)
	}
	events, _ := ReadEvents(dir)
	if got := openIDs(events); got != "" {
		t.Errorf("after dismissal open = %q", got)
	}
	if r := responses(t, dir); len(r) != 1 || r[0].Note != "dismissed: by design" {
		t.Errorf("responses = %+v", r)
	}
}
