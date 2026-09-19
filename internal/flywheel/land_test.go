package flywheel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// appendPassed records the events a task needs to derive status passed.
func appendPassed(t *testing.T, dir, task string) {
	t.Helper()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: task, Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:01:00Z", Task: task, Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
}

// landedEvents returns the task's landed events, newest last.
func landedEvents(t *testing.T, dir, task string) []Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var out []Event
	for _, e := range events {
		if e.Task == task && e.Kind == "landed" {
			out = append(out, e)
		}
	}
	return out
}

func TestLandTaskSuccess(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "merged", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if landed[0].Commit != "abc1234" || landed[0].Note != "merged" {
		t.Errorf("landed event = %+v, want commit abc1234 note merged", landed[0])
	}
	all, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	status := ""
	for _, ts := range Derive(all).Tasks {
		if ts.ID == "T1" {
			status = ts.Status
		}
	}
	if status != "landed" {
		t.Errorf("derived status = %q, want landed", status)
	}
}

// TestLandedTree records the tree of the landed commit, resolved from the
// commit itself rather than the working directory.
func TestLandedTree(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	commit := git(t, dir, []string{"rev-parse", "HEAD"})
	wantTree := git(t, dir, []string{"rev-parse", commit + "^{tree}"})
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", commit, "merged", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if landed[0].Commit != commit {
		t.Errorf("Commit = %q, want %q", landed[0].Commit, commit)
	}
	if landed[0].Tree != wantTree {
		t.Errorf("Tree = %q, want %q", landed[0].Tree, wantTree)
	}
}

// TestLandedTreeMatchesInspected is the comparison the landed tree field
// exists for: a commit carrying flywheel's own bookkeeping (.flywheel/ and
// flywheel.md) alongside a product file must land with the same normalised
// tree treeHash measures, so "what shipped" equals "what was measured".
func TestLandedTreeMatchesInspected(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	log := `{"ts":"2026-09-14T10:00:00Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte(log), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flywheel.md"), []byte("# T1\n"), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "work"})
	commit := git(t, dir, []string{"rev-parse", "HEAD"})
	raw := git(t, dir, []string{"rev-parse", commit + "^{tree}"})
	appendPassed(t, dir, "T1")
	want, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := LandTask(dir, "T1", commit, "merged", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if landed[0].Tree != want {
		t.Errorf("Tree = %q, want %q (treeHash of the same content)", landed[0].Tree, want)
	}
	if landed[0].Tree == raw {
		t.Error("Tree = the unfiltered commit tree; the landed tree must exclude flywheel's own bookkeeping")
	}
}

// TestLandTaskUnresolvableTree lands a commit that is not in any repository
// here; the landed event still succeeds and records an empty tree.
func TestLandTaskUnresolvableTree(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "merged", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if landed[0].Tree != "" {
		t.Errorf("Tree = %q, want empty for an unresolvable commit", landed[0].Tree)
	}
	if landed[0].Commit != "abc1234" {
		t.Errorf("Commit = %q, want abc1234", landed[0].Commit)
	}
}

func TestLandTaskRefusedWithoutPass(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	err := LandTask(dir, "T1", "abc1234", "", false, "")
	if !IsRuleRefusal(err) {
		t.Fatalf("LandTask() error = %v, want a rule refusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("refusal = %v, want rule T5", err)
	}
	if len(landedEvents(t, dir, "T1")) != 0 {
		t.Error("landed event appended despite the refusal")
	}
}

func TestLandTaskBadCommit(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	for _, commit := range []string{"", "abc12", "zzzzzzz", "abcdef1234567890abcdef1234567890abcdef12345678901"} {
		if err := LandTask(dir, "T1", commit, "", false, ""); err == nil {
			t.Errorf("LandTask(commit %q) accepted, want an error", commit)
		}
	}
	if len(landedEvents(t, dir, "T1")) != 0 {
		t.Error("landed event appended despite the bad commit")
	}
}

func TestLandTaskSameCommitNoOp(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "", false, ""); err != nil {
		t.Fatalf("first LandTask() error = %v", err)
	}
	err := LandTask(dir, "T1", "abc1234", "again", false, "")
	if !errors.Is(err, ErrAlreadyLanded) {
		t.Fatalf("second LandTask() error = %v, want ErrAlreadyLanded", err)
	}
	if len(landedEvents(t, dir, "T1")) != 1 {
		t.Errorf("landed events = %d, want 1 (no-op must not append)", len(landedEvents(t, dir, "T1")))
	}
}

func TestLandTaskDifferentCommitRefused(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "", false, ""); err != nil {
		t.Fatalf("first LandTask() error = %v", err)
	}
	err := LandTask(dir, "T1", "def5678", "", false, "")
	if !IsRuleRefusal(err) {
		t.Fatalf("second LandTask() error = %v, want a rule refusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("refusal = %v, want rule T5", err)
	}
	if len(landedEvents(t, dir, "T1")) != 1 {
		t.Errorf("landed events = %d, want 1", len(landedEvents(t, dir, "T1")))
	}
}

// TestLandTaskOrdinaryRecordsNoLeadFlag checks an ordinary landing's event
// carries no lead_implemented field and its note is unchanged.
func TestLandTaskOrdinaryRecordsNoLeadFlag(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "merged", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if landed[0].LeadImplemented {
		t.Error("LeadImplemented = true, want false")
	}
	if landed[0].Note != "merged" {
		t.Errorf("Note = %q, want %q", landed[0].Note, "merged")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if bytes.Contains(raw, []byte("lead_implemented")) {
		t.Error("ordinary landing JSON carries a lead_implemented field")
	}
}

// TestLandTaskLeadImplemented checks a --by-lead landing records the flag and
// the reason, prefixed in the note.
func TestLandTaskLeadImplemented(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "", true, "40-line script fix, faster than a worker round-trip"); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	if !landed[0].LeadImplemented {
		t.Error("LeadImplemented = false, want true")
	}
	want := "lead-implemented: 40-line script fix, faster than a worker round-trip"
	if landed[0].Note != want {
		t.Errorf("Note = %q, want %q", landed[0].Note, want)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"lead_implemented":true`)) {
		t.Errorf("lead-implemented landing JSON missing %q:\n%s", `"lead_implemented":true`, raw)
	}
}

// TestLandTaskLeadImplementedComposesNote checks an operator note stays first
// and the reason is appended after "; ".
func TestLandTaskLeadImplementedComposesNote(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	if err := LandTask(dir, "T1", "abc1234", "merged", true, "script fix"); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
	want := "merged; lead-implemented: script fix"
	if landed[0].Note != want {
		t.Errorf("Note = %q, want %q", landed[0].Note, want)
	}
}

// TestLandTaskExceptionLandsUnpassedTask checks a planned-only task can land
// on an exception, recording both excepted and landed events.
func TestLandTaskExceptionLandsUnpassedTask(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "ran go test by hand", "lead-1"); err != nil {
		t.Fatalf("LandTaskWithException() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var excepted, landed *Event
	for i, e := range events {
		if e.Task == "T1" && e.Kind == "excepted" {
			excepted = &events[i]
		}
		if e.Task == "T1" && e.Kind == "landed" {
			landed = &events[i]
		}
	}
	if excepted == nil {
		t.Fatal("no excepted event found")
	}
	if landed == nil {
		t.Fatal("no landed event found")
	}
	if excepted.Note != "ran go test by hand" {
		t.Errorf("excepted Note = %q, want %q", excepted.Note, "ran go test by hand")
	}
	if excepted.Session != "lead-1" {
		t.Errorf("excepted Session = %q, want %q", excepted.Session, "lead-1")
	}
	if excepted.Reason != "status planned" {
		t.Errorf("excepted Reason = %q, want %q", excepted.Reason, "status planned")
	}
	landedNote := landedEvents(t, dir, "T1")[0].Note
	if !bytes.HasPrefix([]byte(landedNote), []byte("exception: ")) {
		t.Errorf("landed Note should start with 'exception: ', got %q", landedNote)
	}
}

// TestLandTaskExceptionRefusedForWorkerSession checks an exception from a
// worker session is refused with rule T4.
func TestLandTaskExceptionRefusedForWorkerSession(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Session: "w1"}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}
	err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "ran go test by hand", "w1")
	if !IsRuleRefusal(err) {
		t.Fatalf("LandTaskWithException() error = %v, want a rule refusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T4" {
		t.Errorf("refusal = %v, want rule T4", err)
	}
	if len(landedEvents(t, dir, "T1")) != 0 {
		t.Error("landed event appended despite the refusal")
	}
}

// TestLandTaskExceptionRefusedWhenPassed checks an exception is refused when
// the task is already passed.
func TestLandTaskExceptionRefusedWhenPassed(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "ran go test by hand", "lead-1")
	if !IsRuleRefusal(err) {
		t.Fatalf("LandTaskWithException() error = %v, want a rule refusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("refusal = %v, want rule T5", err)
	}
	if len(landedEvents(t, dir, "T1")) != 0 {
		t.Error("landed event appended despite the refusal")
	}
}

// TestLandTaskExceptionRefusedForUnknownTask checks an exception is refused
// for a task with no events.
func TestLandTaskExceptionRefusedForUnknownTask(t *testing.T) {
	dir := t.TempDir()
	err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "ran go test by hand", "lead-1")
	if !IsRuleRefusal(err) {
		t.Fatalf("LandTaskWithException() error = %v, want a rule refusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("refusal = %v, want rule T5", err)
	}
	if len(landedEvents(t, dir, "T1")) != 0 {
		t.Error("landed event appended despite the refusal")
	}
}

// TestValidateExceptedRequiresNoteAndSession checks Validate rejects excepted
// events without a note or session.
func TestValidateExceptedRequiresNoteAndSession(t *testing.T) {
	err := Validate(Event{Task: "T1", Kind: "excepted", Note: "", Session: "lead-1"})
	if err == nil {
		t.Error("Validate() accepted excepted event without note")
	}
	err = Validate(Event{Task: "T1", Kind: "excepted", Note: "evidence", Session: ""})
	if err == nil {
		t.Error("Validate() accepted excepted event without session")
	}
	err = Validate(Event{Task: "T1", Kind: "excepted", Note: "evidence", Session: "lead-1"})
	if err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

// TestLandTaskConcurrentDifferentCommitsLandOnce races two landings of one
// passed task under different commits: the dispatch lock serialises the
// read-check-append, so exactly one lands and the other is refused by T5.
func TestLandTaskConcurrentDifferentCommitsLandOnce(t *testing.T) {
	dir := t.TempDir()
	appendPassed(t, dir, "T1")
	commits := []string{"abc1234", "def5678"}
	errs := make([]error, len(commits))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, c := range commits {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			<-start
			errs[i] = LandTask(dir, "T1", c, "", false, "")
		}(i, c)
	}
	close(start)
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case IsRuleRefusal(err):
			refused++
		default:
			t.Errorf("LandTask() unexpected error = %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("landings ok=%d refused=%d, want exactly one of each (errs %v)", ok, refused, errs)
	}
	if n := len(landedEvents(t, dir, "T1")); n != 1 {
		t.Errorf("landed events = %d, want 1", n)
	}
}
