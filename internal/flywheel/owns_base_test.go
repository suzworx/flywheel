package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOwnsBaseCommittedChangeCounts checks that a committed change counts when
// base = HEAD.
func TestOwnsBaseCommittedChangeCounts(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	head := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	git(t, dir, []string{"add", "b.go"})
	git(t, dir, []string{"commit", "-m", "add b"})
	changed, err := unitChangedPaths(dir, head)
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "b.go") {
		t.Errorf("changed = %v, want b.go", changed)
	}
}

// TestOwnsBaseUncommittedStillCounts checks that uncommitted changes count
// when base = HEAD.
func TestOwnsBaseUncommittedStillCounts(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	head := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify a.go: %v", err)
	}
	changed, err := unitChangedPaths(dir, head)
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "a.go") {
		t.Errorf("changed = %v, want a.go", changed)
	}
}

// TestOwnsBaseMergedBranchExcluded checks that files from a merged branch are
// excluded when they arrived by merging, not by the unit's own commits.
func TestOwnsBaseMergedBranchExcluded(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mainBranch := strings.TrimSpace(git(t, dir, []string{"rev-parse", "--abbrev-ref", "HEAD"}))
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"checkout", "-b", "x"})
	if err := os.WriteFile(filepath.Join(dir, "c.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write c.go: %v", err)
	}
	git(t, dir, []string{"add", "c.go"})
	git(t, dir, []string{"commit", "-m", "add c"})
	git(t, dir, []string{"checkout", mainBranch})
	if err := os.WriteFile(filepath.Join(dir, "d.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write d.go: %v", err)
	}
	git(t, dir, []string{"add", "d.go"})
	git(t, dir, []string{"commit", "-m", "add d"})
	git(t, dir, []string{"merge", "--no-ff", "x", "-m", "merge x"})
	changed, err := unitChangedPaths(dir, base)
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "d.go") {
		t.Errorf("changed = %v, want d.go", changed)
	}
	if hasPath(changed, "c.go") {
		t.Errorf("changed = %v, want c.go excluded (merged)", changed)
	}
}

// TestOwnsBaseEmptyBaseFallsBack checks that with an empty base, only
// uncommitted/untracked paths are listed (the old behavior).
func TestOwnsBaseEmptyBaseFallsBack(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	git(t, dir, []string{"add", "b.go"})
	git(t, dir, []string{"commit", "-m", "add b"})
	changed, err := unitChangedPaths(dir, "")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if hasPath(changed, "b.go") {
		t.Errorf("changed = %v, want b.go excluded (empty base)", changed)
	}
}

// TestOwnsBaseValidateFlagsCommittedStray checks that a stray committed change
// is flagged by ValidateTask.
func TestOwnsBaseValidateFlagsCommittedStray(t *testing.T) {
	dir, err := initTask(t, []string{"true"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := AppendEvent(dir, Event{
		TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1",
		Base: base,
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: "2026-09-19T00:01:00Z", Task: "T1", Kind: "finished", Attempt: "r1",
		Reason: "stop",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write stray.go: %v", err)
	}
	git(t, dir, []string{"add", "stray.go"})
	git(t, dir, []string{"commit", "-m", "stray"})
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OK() {
		t.Error("OK() = true, want false: stray.go is outside owns")
	}
	if !hasPath(res.Outside, "stray.go") {
		t.Errorf("Outside = %v, want stray.go", res.Outside)
	}
}

// hasPath reports whether v is in ss.
func hasPath(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
