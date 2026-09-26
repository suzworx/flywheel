package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deltaHeaderTask is setupTask with a brief that declares two owns, one gate
// and no needs-state, so the inherited counts in the warning are checkable.
func deltaHeaderTask(t *testing.T) string {
	t.Helper()
	dir := setupTask(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: a.go, b.go\nneeds: none\ngate: true\n\n# TASK: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runDelta writes delta as the correction prompt and runs it, returning the
// progress output.
func runDelta(t *testing.T, dir, delta string, worktree bool) string {
	t.Helper()
	path := filepath.Join(dir, ".flywheel", "T1.delta.txt")
	if err := os.WriteFile(path, []byte(delta), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: path, Worktree: worktree, Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return buf.String()
}

// TestDeltaHeaderMissingWarns checks a correction delta with no brief header
// prints one warning naming what it inherits (issue #472).
func TestDeltaHeaderMissingWarns(t *testing.T) {
	t.Parallel()
	dir := deltaHeaderTask(t)
	out := runDelta(t, dir, "fix the registry\n", false)
	want := "has no brief header; it inherits the base brief's owns (2 paths), gates (1) and needs-state links (0); add an owns: line to the delta to widen owns"
	if strings.Count(out, "warning: T1 c1: delta ") != 1 || !strings.Contains(out, want) {
		t.Errorf("progress = %q, want one warning containing %q", out, want)
	}
}

// TestDeltaHeaderOwnsNoWarning checks a delta that starts with an owns: line
// does not warn (issue #472).
func TestDeltaHeaderOwnsNoWarning(t *testing.T) {
	t.Parallel()
	dir := deltaHeaderTask(t)
	out := runDelta(t, dir, "owns: c.go\n\nfix the registry\n", false)
	if strings.Contains(out, "no brief header") {
		t.Errorf("progress = %q, want no header warning", out)
	}
}

// TestDeltaHeaderMissingLinksBaseNeedsState checks a header-less delta on a
// fresh worktree still links the base brief's needs-state "(link)" path
// (issue #472).
func TestDeltaHeaderMissingLinksBaseNeedsState(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\nstate/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: a.go\nneeds: none\nneeds-state: state/ (link)\n\n# TASK: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "db"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "needs-state"})

	out := runDelta(t, dir, "fix the registry\n", true)
	if !strings.Contains(out, "needs-state links (1)") {
		t.Errorf("progress = %q, want the warning to count 1 needs-state link", out)
	}
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "state", "db")); err != nil {
		t.Errorf("state/db not reachable in the worktree: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "worktree_setup" && strings.Join(e.Linked, ",") == "state/" {
			linked = true
		}
	}
	if !linked {
		t.Error("no worktree_setup event with Linked [state/]")
	}
}
