package flywheel

import (
	"os"
	"os/exec"
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
	changed, err := unitChangedPaths(dir, head, "T1")
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
	changed, err := unitChangedPaths(dir, head, "T1")
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
	changed, err := unitChangedPaths(dir, base, "T1")
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
	changed, err := unitChangedPaths(dir, "", "T1")
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

// TestOwnsBaseOtherTaskCommitExcluded checks that a commit whose
// Flywheel-Task trailer names another unit does not count (#338 review).
func TestOwnsBaseOtherTaskCommitExcluded(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	for _, c := range []struct{ file, task string }{{"mine.go", "T1"}, {"theirs.go", "T2"}, {"plain.go", ""}} {
		if err := os.WriteFile(filepath.Join(dir, c.file), []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", c.file, err)
		}
		git(t, dir, []string{"add", c.file})
		msg := "add " + c.file
		if c.task != "" {
			msg += "\n\nFlywheel-Task: " + c.task
		}
		git(t, dir, []string{"commit", "-m", msg})
	}
	changed, err := unitChangedPaths(dir, base, "T1")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "mine.go") || !hasPath(changed, "plain.go") {
		t.Errorf("changed = %v, want mine.go and plain.go", changed)
	}
	if hasPath(changed, "theirs.go") {
		t.Errorf("changed = %v, want theirs.go excluded (Flywheel-Task: T2)", changed)
	}
}

// TestOwnsBaseMergeResolutionCounts checks that an edit made while resolving
// a merge conflict counts, while the merged branch's own files do not.
func TestOwnsBaseMergeResolutionCounts(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mainBranch := strings.TrimSpace(git(t, dir, []string{"rev-parse", "--abbrev-ref", "HEAD"}))
	if err := os.WriteFile(filepath.Join(dir, "shared.go"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write shared.go: %v", err)
	}
	git(t, dir, []string{"add", "shared.go"})
	git(t, dir, []string{"commit", "-m", "shared"})
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"checkout", "-b", "x"})
	for name, body := range map[string]string{"shared.go": "theirs\n", "other.go": "package x\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, dir, []string{"add", "shared.go", "other.go"})
	git(t, dir, []string{"commit", "-m", "x"})
	git(t, dir, []string{"checkout", mainBranch})
	if err := os.WriteFile(filepath.Join(dir, "shared.go"), []byte("ours\n"), 0o644); err != nil {
		t.Fatalf("write shared.go: %v", err)
	}
	git(t, dir, []string{"commit", "-am", "ours"})
	gitMayFail(t, dir, []string{"merge", "--no-ff", "x", "-m", "merge x"})
	if err := os.WriteFile(filepath.Join(dir, "shared.go"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("resolve shared.go: %v", err)
	}
	git(t, dir, []string{"add", "shared.go"})
	git(t, dir, []string{"commit", "--no-edit"})
	// A clean tree: only the commits can report shared.go.
	changed, err := unitChangedPaths(dir, base, "T1")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "shared.go") {
		t.Errorf("changed = %v, want shared.go (edited on the branch and in the resolution)", changed)
	}
	if hasPath(changed, "other.go") {
		t.Errorf("changed = %v, want other.go excluded (merged in cleanly)", changed)
	}
}

// TestOwnsBaseEvilMergeCounts checks that a file changed only by the merge
// commit itself (in neither parent) counts.
func TestOwnsBaseEvilMergeCounts(t *testing.T) {
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
	git(t, dir, []string{"merge", "--no-ff", "--no-commit", "x"})
	if err := os.WriteFile(filepath.Join(dir, "evil.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write evil.go: %v", err)
	}
	git(t, dir, []string{"add", "evil.go"})
	git(t, dir, []string{"commit", "-m", "merge x"})
	changed, err := unitChangedPaths(dir, base, "T1")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	if !hasPath(changed, "evil.go") {
		t.Errorf("changed = %v, want evil.go (added by the merge commit)", changed)
	}
	if hasPath(changed, "c.go") {
		t.Errorf("changed = %v, want c.go excluded (merged)", changed)
	}
}

// TestOwnsBaseRenameSourceCounts checks that renaming a file reports both the
// source and the destination, committed or not (#338 review).
func TestOwnsBaseRenameSourceCounts(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"mv", "a.go", "moved.go"})
	git(t, dir, []string{"commit", "-m", "move"})
	if err := os.WriteFile(filepath.Join(dir, "second.go"), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatalf("write second.go: %v", err)
	}
	git(t, dir, []string{"add", "second.go"})
	git(t, dir, []string{"commit", "-m", "second"})
	git(t, dir, []string{"mv", "second.go", "third.go"}) // staged, uncommitted
	changed, err := unitChangedPaths(dir, base, "T1")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	for _, want := range []string{"a.go", "moved.go", "second.go", "third.go"} {
		if !hasPath(changed, want) {
			t.Errorf("changed = %v, want %s", changed, want)
		}
	}
}

// TestOwnsBaseUnusualNames checks that paths git would quote (spaces,
// non-ASCII) come back as their real names.
func TestOwnsBaseUnusualNames(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	names := []string{"with space.go", "naïve.go"}
	for _, n := range names[:1] {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
		git(t, dir, []string{"add", n})
	}
	git(t, dir, []string{"commit", "-m", "committed"})
	if err := os.WriteFile(filepath.Join(dir, names[1]), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", names[1], err)
	}
	changed, err := unitChangedPaths(dir, base, "T1")
	if err != nil {
		t.Fatalf("unitChangedPaths() error = %v", err)
	}
	for _, want := range names {
		if !hasPath(changed, want) {
			t.Errorf("changed = %q, want %q", changed, want)
		}
	}
}

// gitMayFail runs git in dir and ignores its exit status (a merge that stops
// on a conflict).
func gitMayFail(t *testing.T, dir string, args []string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	_ = cmd.Run()
}
