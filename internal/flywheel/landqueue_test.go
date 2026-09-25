package flywheel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLandMergeFastForwards tests that LandMerge succeeds when main advanced
// by an unrelated commit meanwhile.
func TestLandMergeFastForwards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Initialize flywheel
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Create and plan a task
	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	// Create feature.go
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	// Write config to allow running
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Create worktree
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	// Record the dispatch event with worktree
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	// Make a change in the worktree
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write feature.go in worktree: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "feature"})

	// Record passing inspection
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	// Advance main with an unrelated commit
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write other.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "other change"})

	mainHeadBefore := git(t, dir, []string{"rev-parse", "HEAD"})

	// Land the merge
	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("LandMerge() error = %v", err)
	}

	// Check results
	if result.Commit == "" {
		t.Errorf("Commit is empty")
	}
	if !result.Rebased {
		t.Errorf("Rebased = false, want true")
	}
	if len(result.Conflict) > 0 {
		t.Errorf("Conflict = %v, want empty", result.Conflict)
	}

	mainHeadAfter := git(t, dir, []string{"rev-parse", "HEAD"})
	if result.Commit != mainHeadAfter {
		t.Errorf("result.Commit = %q, HEAD = %q", result.Commit, mainHeadAfter)
	}

	// Check that main's HEAD advanced
	if mainHeadBefore == mainHeadAfter {
		t.Errorf("main's HEAD did not advance")
	}

	// Check that worktree is gone
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("worktree still exists")
	}

	// Check landed event
	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
}

// TestLandMergeConflictWritesDelta tests that a rebase conflict writes a
// correction brief and returns the correct status.
func TestLandMergeConflictWritesDelta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Initialize flywheel
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Create and plan a task
	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	// Create feature.go
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	// Write config
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Create worktree
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	// Record dispatch
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	// Make a change in the worktree
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("worker change\n"), 0o644); err != nil {
		t.Fatalf("write feature.go in worktree: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "worker work"})

	// Record passing inspection
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	// Change feature.go on main (conflict)
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("main change\n"), 0o644); err != nil {
		t.Fatalf("write feature.go on main: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "main change"})

	mainHeadBefore := git(t, dir, []string{"rev-parse", "HEAD"})

	// Attempt land
	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err == nil {
		t.Errorf("LandMerge() succeeded, want error")
	}
	if !IsRuleRefusal(err) {
		t.Errorf("error is not RuleRefusal: %v", err)
	}

	// Check results
	if len(result.Conflict) == 0 {
		t.Errorf("Conflict is empty")
	}
	if !strings.Contains(strings.Join(result.Conflict, ","), "feature.go") {
		t.Errorf("Conflict does not contain feature.go: %v", result.Conflict)
	}
	if result.Delta == "" {
		t.Errorf("Delta is empty")
	}

	// Check delta file exists
	deltaPath := filepath.Join(dir, result.Delta)
	if _, err := os.Stat(deltaPath); err != nil {
		t.Errorf("delta file not found: %v", err)
	}

	// Check delta content
	deltaContent, err := os.ReadFile(deltaPath)
	if err != nil {
		t.Fatalf("read delta: %v", err)
	}
	if !bytes.Contains(deltaContent, []byte("owns: feature.go")) {
		t.Errorf("delta does not contain 'owns: feature.go'")
	}
	if !bytes.Contains(deltaContent, []byte("conflicts in: feature.go")) {
		t.Errorf("delta does not contain 'conflicts in: feature.go'")
	}

	// Check main's HEAD unchanged
	mainHeadAfter := git(t, dir, []string{"rev-parse", "HEAD"})
	if mainHeadBefore != mainHeadAfter {
		t.Errorf("main's HEAD changed during conflict")
	}

	// The worktree holds a merge in progress on the unit's own branch, with
	// the markers for a worker to resolve (a worker can only edit files).
	if got := git(t, wt, []string{"rev-parse", "--abbrev-ref", "HEAD"}); got != "fw/T1" {
		t.Errorf("worktree branch = %q, want fw/T1 still checked out", got)
	}
	git(t, wt, []string{"rev-parse", "-q", "--verify", "MERGE_HEAD"})
	if b, _ := os.ReadFile(filepath.Join(wt, "feature.go")); !bytes.Contains(b, []byte("<<<<<<<")) {
		t.Errorf("feature.go has no conflict markers:\n%s", b)
	}

	// Landing again before the merge is finished is refused, saying how.
	if _, err := LandMerge("T1", LandMergeOptions{Dir: dir}); !IsRuleRefusal(err) || !strings.Contains(err.Error(), "merge is in progress") {
		t.Errorf("LandMerge() mid-merge error = %v, want a refusal naming the merge in progress", err)
	}

	// Resolve (as the correction worker would), commit the merge, land.
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("resolve feature.go: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "--no-edit"})
	result, err = LandMerge("T1", LandMergeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("LandMerge() after resolving error = %v", err)
	}
	if result.Rebased {
		t.Error("Rebased = true, want false: the merge already contains main")
	}
	if got := git(t, dir, []string{"rev-parse", "HEAD"}); got != result.Commit {
		t.Errorf("main HEAD = %s, want %s", got, result.Commit)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "feature.go")); string(b) != "resolved\n" {
		t.Errorf("main feature.go = %q, want the resolution", b)
	}
}

// TestLandMergeNotPassedRefused tests that a task without passing status
// is refused.
func TestLandMergeNotPassedRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	// Initialize flywheel
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	// Don't add an inspection event, so status is not passed

	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err == nil {
		t.Errorf("LandMerge() succeeded, want error")
	}
	if !IsRuleRefusal(err) {
		t.Errorf("error is not RuleRefusal: %v", err)
	}
	rf := err.(*RuleRefusal)
	if rf.Rule != "T5" {
		t.Errorf("Rule = %q, want T5", rf.Rule)
	}
	if result.Commit != "" {
		t.Errorf("Commit should be empty on refusal")
	}
}

// TestLandMergeNoWorktreeRefused tests that a task without a worktree is
// refused.
func TestLandMergeNoWorktreeRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	// Dispatch without a worktree
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	// Record pass
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err == nil {
		t.Errorf("LandMerge() succeeded, want error")
	}
	rf, ok := err.(*RuleRefusal)
	if !ok {
		t.Errorf("error is not RuleRefusal: %v", err)
		return
	}
	if rf.Rule != "land" {
		t.Errorf("Rule = %q, want land", rf.Rule)
	}
	if result.Commit != "" {
		t.Errorf("Commit should be empty on refusal")
	}
}

// TestLandMergeDirtyWorktreeRefused tests that a worktree with uncommitted
// changes is refused.
func TestLandMergeDirtyWorktreeRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "work"})

	// Leave an uncommitted change
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("make dirty: %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err == nil {
		t.Errorf("LandMerge() succeeded, want error")
	}
	rf, ok := err.(*RuleRefusal)
	if !ok {
		t.Errorf("error is not RuleRefusal: %v", err)
		return
	}
	if rf.Rule != "land" {
		t.Errorf("Rule = %q, want land", rf.Rule)
	}
	if !strings.Contains(rf.Fix, "feature.go") {
		t.Errorf("Fix does not mention feature.go: %v", rf.Fix)
	}
	if result.Commit != "" {
		t.Errorf("Commit should be empty on refusal")
	}
}

// TestLandMergeGateFailsRefused tests that gates failing after rebase are
// refused.
func TestLandMergeGateFailsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	brief := "owns: feature.go\nneeds: none\ngate: test ! -f blocker.txt\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "feature"})

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	// Add blocker.txt to main (gate will fail after rebase)
	if err := os.WriteFile(filepath.Join(dir, "blocker.txt"), []byte("blocker\n"), 0o644); err != nil {
		t.Fatalf("write blocker.txt: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "add blocker"})

	mainHeadBefore := git(t, dir, []string{"rev-parse", "HEAD"})

	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err == nil {
		t.Errorf("LandMerge() succeeded, want error")
	}
	rf, ok := err.(*RuleRefusal)
	if !ok {
		t.Errorf("error is not RuleRefusal: %v", err)
		return
	}
	if rf.Rule != "land" {
		t.Errorf("Rule = %q, want land", rf.Rule)
	}
	if !strings.Contains(rf.Fix, "gates fail") {
		t.Errorf("Fix does not mention gate failure: %v", rf.Fix)
	}

	// Main's HEAD should be unchanged
	mainHeadAfter := git(t, dir, []string{"rev-parse", "HEAD"})
	if mainHeadBefore != mainHeadAfter {
		t.Errorf("main's HEAD changed after gate failure")
	}

	if result.Commit != "" {
		t.Errorf("Commit should be empty on refusal")
	}
}

// TestLandMergeAlreadyUpToDate tests that when main did not move, Rebased
// is false but the landing succeeds.
func TestLandMergeAlreadyUpToDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	brief := "owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "initial"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("change\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "work"})

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}

	// Don't add any new commits to main

	result, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	if err != nil {
		t.Fatalf("LandMerge() error = %v", err)
	}

	if result.Commit == "" {
		t.Errorf("Commit is empty")
	}
	if result.Rebased {
		t.Errorf("Rebased = true, want false (main did not move)")
	}
	if len(result.Conflict) > 0 {
		t.Errorf("Conflict = %v, want empty", result.Conflict)
	}

	mainHeadAfter := git(t, dir, []string{"rev-parse", "HEAD"})
	if result.Commit != mainHeadAfter {
		t.Errorf("result.Commit = %q, HEAD = %q", result.Commit, mainHeadAfter)
	}

	landed := landedEvents(t, dir, "T1")
	if len(landed) != 1 {
		t.Fatalf("landed events = %d, want 1", len(landed))
	}
}

// landMergeSetup builds a repo with a flywheel dir, a passed task T1 that
// ran in its worktree on fw/T1 and committed feature.go there, and main
// advanced by an unrelated commit; it returns the root and the worktree.
func landMergeSetup(t *testing.T) (dir, wt string) {
	t.Helper()
	dir = t.TempDir()
	landRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: feature.go\nneeds: none\ngate: exit 0\n\n# TASK: test\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var err error
	if wt, err = TaskWorktree(dir, "T1"); err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Workdir: wt}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git(t, wt, []string{"add", "-A"})
	git(t, wt, []string{"commit", "-m", "feature"})
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write other.go: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "other"})
	return dir, wt
}

// TestLandMergeRefusalKeepsBranch checks that a landing rule refusing after
// the gates (T9: an untriaged signal) leaves the integration branch where it
// was, and that --allow-untriaged then lands (#345 review).
func TestLandMergeRefusalKeepsBranch(t *testing.T) {
	t.Parallel()
	dir, _ := landMergeSetup(t)
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:03:00Z", Task: "T1", Kind: "signal", Signal: "no-plan", Attempt: "r1"}); err != nil {
		t.Fatalf("append signal: %v", err)
	}
	before := git(t, dir, []string{"rev-parse", "HEAD"})
	_, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T9" {
		t.Fatalf("LandMerge() error = %v, want a T9 refusal", err)
	}
	if after := git(t, dir, []string{"rev-parse", "HEAD"}); after != before {
		t.Errorf("main moved from %s to %s on a refused landing", before, after)
	}
	res, err := LandMerge("T1", LandMergeOptions{Dir: dir, AllowUntriaged: "reviewed the no-plan signal"})
	if err != nil {
		t.Fatalf("LandMerge(AllowUntriaged) error = %v", err)
	}
	if got := git(t, dir, []string{"rev-parse", "HEAD"}); got != res.Commit || got == before {
		t.Errorf("main HEAD = %s, want the landed commit %s", got, res.Commit)
	}
}

// TestLandMergeWrongBranchRefused checks that a worktree switched away from
// fw/<task> is refused instead of landing another branch (#345 review).
func TestLandMergeWrongBranchRefused(t *testing.T) {
	t.Parallel()
	dir, wt := landMergeSetup(t)
	git(t, wt, []string{"checkout", "-q", "-b", "experiment"})
	before := git(t, dir, []string{"rev-parse", "HEAD"})
	_, err := LandMerge("T1", LandMergeOptions{Dir: dir})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "land" || !strings.Contains(r.Fix, "fw/T1") {
		t.Fatalf("LandMerge() error = %v, want a land refusal naming fw/T1", err)
	}
	if after := git(t, dir, []string{"rev-parse", "HEAD"}); after != before {
		t.Errorf("main moved on a refused landing")
	}
}

// TestLandMergeOntoMustBeABranch checks that --onto is never passed to git
// as an option and must name a local branch (#345 review).
func TestLandMergeOntoMustBeABranch(t *testing.T) {
	t.Parallel()
	dir, _ := landMergeSetup(t)
	for _, onto := range []string{"--exec=touch pwned", "-x", "no-such-branch"} {
		_, err := LandMerge("T1", LandMergeOptions{Dir: dir, Onto: onto})
		var r *RuleRefusal
		if !errors.As(err, &r) || r.Rule != "land" {
			t.Errorf("Onto %q: error = %v, want a land refusal", onto, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("an option-shaped --onto reached git")
	}
}

// landRepo is initRepo plus a repo-local test identity: LandMerge runs plain
// git (rebase, merge) that commits, and CI machines have no global identity.
func landRepo(t *testing.T, dir string) {
	t.Helper()
	initRepo(t, dir)
	git(t, dir, []string{"config", "user.name", "test"})
	git(t, dir, []string{"config", "user.email", "test@example.com"})
}
