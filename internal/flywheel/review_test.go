package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewRefusedBadVerdict(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	_, err = ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "bogus", Session: "r1"})
	if err == nil {
		t.Fatal("ReviewTask() accepted a bogus verdict")
	}
	if got := refusalRule(t, err); got != "T8" {
		t.Errorf("rule = %q, want T8", got)
	}
}

func TestReviewRefusedEmptySession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	_, err = ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: ""})
	if err == nil {
		t.Fatal("ReviewTask() accepted an empty session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestReviewRefusedFromWorkerSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	_, err = ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "w1"})
	if err == nil {
		t.Fatal("ReviewTask() accepted a pass from a worker session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestReviewOwnsRefusalNamesOutsidePath(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write stray.txt: %v", err)
	}
	_, err = ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1"})
	if err == nil {
		t.Fatal("ReviewTask() accepted a change outside owns")
	}
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a RuleRefusal", err)
	}
	if r.Rule != "owns" {
		t.Errorf("rule = %q, want owns", r.Rule)
	}
	if !strings.Contains(r.Fix, "stray.txt") {
		t.Errorf("fix = %q, want it to name stray.txt", r.Fix)
	}
}

func TestReviewFailingGateIsT3(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 1"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	_, err = ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1"})
	if err == nil {
		t.Fatal("ReviewTask() accepted a failing gate")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
	var r *RuleRefusal
	errors.As(err, &r)
	if !strings.Contains(r.Fix, "2") || !strings.Contains(r.Fix, "exit 1") {
		t.Errorf("fix = %q, want it to name gate 2 and its command", r.Fix)
	}
}

func TestReviewSuccessRecordsPassAndDerivesStatus(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	res, err := ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1", Note: "looks good"})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if res.Tree == "" {
		t.Error("empty tree hash")
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "reviewed" {
			found = true
			if e.Verdict != "pass" || e.Session != "r1" || e.Persona != "reviewer" || e.Note != "looks good" || e.Tree != res.Tree {
				t.Errorf("reviewed event = %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("no reviewed event recorded")
	}
	st := Derive(evs)
	for _, ts := range st.Tasks {
		if ts.ID == "T1" && ts.Status != "passed" {
			t.Errorf("status = %q, want passed", ts.Status)
		}
	}
}

func TestReviewChecklistRecordsCountAndKeepsNote(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	res, err := ReviewTask(dir, "T1", ReviewOptions{
		Dir: dir, Verdict: "pass", Session: "r1", Note: "looks good",
		Checklist: []string{"migration is backward compatible", "no PII in new columns"},
	})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if len(res.Checklist) != 2 {
		t.Errorf("Checklist = %v, want 2 entries", res.Checklist)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "reviewed" {
			found = true
			want := "looks good; checklist: 2 confirmed"
			if e.Note != want {
				t.Errorf("note = %q, want %q", e.Note, want)
			}
		}
	}
	if !found {
		t.Fatal("no reviewed event recorded")
	}
}

func TestReviewRemovesTempWorktree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	scoped := t.TempDir()
	t.Setenv("TMP", scoped)
	t.Setenv("TEMP", scoped)
	t.Setenv("TMPDIR", scoped)
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "fw-review-*"))
	if _, err := ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1"}); err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "fw-review-*"))
	if len(after) != len(before) {
		t.Errorf("temp worktrees leaked: before %d after %d", len(before), len(after))
	}
}

func TestReviewReportsFinishedReasonAndReportPath(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc, Reason: "stop"}); err != nil {
		t.Fatalf("AppendEvent(finished) error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:01:00Z", Task: "T1", Kind: "report", Path: ".flywheel/runs/T1.r1.report.md"}); err != nil {
		t.Fatalf("AppendEvent(report) error = %v", err)
	}
	res, err := ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1"})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if !res.Finished || res.FinishedReason != "stop" {
		t.Errorf("finished = %v %q, want true \"stop\"", res.Finished, res.FinishedReason)
	}
	if !res.Report || res.ReportPath != ".flywheel/runs/T1.r1.report.md" {
		t.Errorf("report = %v %q, want true \".flywheel/runs/T1.r1.report.md\"", res.Report, res.ReportPath)
	}
}

func TestReviewNoFinishedOrReportEvent(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Verdict: "pass", Session: "r1"})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if res.Finished || res.Report {
		t.Errorf("finished/report = %v/%v, want false/false with no events", res.Finished, res.Report)
	}
}

// TestReviewAppliesUncommittedChangesAndUntrackedFiles checks that isolation
// carries the workdir's own uncommitted edits and new files into the
// isolated tree, not just what HEAD has committed: dir (the flywheel root)
// and workdir (the git tree under review) are deliberately separate, as
// flywheel run's own worktrees are.
func TestReviewAppliesUncommittedChangesAndUntrackedFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	workdir := t.TempDir()
	initRepo(t, workdir)
	brief := "owns: a.go, newfile.txt\nneeds: none\n" +
		"gate: grep -q added a.go\ngate: test -f newfile.txt\n\n# TASK: review\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("AppendEvent(planned) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "a.go"), []byte("package x\nadded\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "newfile.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatalf("write newfile.txt: %v", err)
	}
	res, err := ReviewTask(dir, "T1", ReviewOptions{Dir: dir, Workdir: workdir, Verdict: "pass", Session: "r1"})
	if err != nil {
		t.Fatalf("ReviewTask() error = %v", err)
	}
	if res.Tree == "" {
		t.Error("empty tree hash")
	}
}
