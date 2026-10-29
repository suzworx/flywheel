package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunResumeInTaskWorktree checks a --delta with neither --worktree nor
// --workdir that continues in the task's own worktree runs as --worktree, so
// its clean stop is committed on fw/<task> (issue #894), while one that
// continues in a --workdir tree (issue #545) still commits nothing. The
// owned change is written before each dispatch: the sim worker writes none.
func TestRunResumeInTaskWorktree(t *testing.T) {
	t.Parallel()
	writeDeltaFile := func(t *testing.T) string {
		t.Helper()
		delta := filepath.Join(t.TempDir(), "delta.txt")
		if err := os.WriteFile(delta, []byte("owns: a.go\nneeds: none\n\n# TASK: fix\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return delta
	}
	write := func(t *testing.T, tree, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tree, "a.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("task worktree", func(t *testing.T) {
		t.Parallel()
		dir := worktreeRepo(t) // T1 owns a.go
		wt, err := TaskWorktree(dir, "T1")
		if err != nil {
			t.Fatal(err)
		}
		write(t, wt, "package a\n")
		if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
			t.Fatalf("Run() r1 error = %v", err)
		}
		if r1 := lastFinished(t, dir, "T1"); !CommitOK(r1.Commit) {
			t.Fatalf("r1 finished commit = %q, want one", r1.Commit)
		}
		write(t, wt, "package a // c1\n")
		var buf strings.Builder
		if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: writeDeltaFile(t), Progress: &buf}); err != nil {
			t.Fatalf("Run() delta error = %v", err)
		}
		if !strings.Contains(buf.String(), "T1 continuing in its task worktree: run as --worktree (issue #894)") {
			t.Errorf("progress = %q, want the continuing-in-task-worktree line", buf.String())
		}
		fin := lastFinished(t, dir, "T1")
		if fin.Attempt != "c1" || fin.Reason != "stop" || !CommitOK(fin.Commit) {
			t.Fatalf("finished attempt=%q reason=%q commit=%q, want c1 stop with a commit", fin.Attempt, fin.Reason, fin.Commit)
		}
		if log := git(t, wt, []string{"log", "--format=%H", "fw/T1"}); !strings.Contains(log, fin.Commit) {
			t.Errorf("fw/T1 log %q lacks the c1 commit %s", log, fin.Commit)
		}
		if st := strings.TrimSpace(git(t, wt, []string{"status", "--porcelain", "--", "a.go"})); st != "" {
			t.Errorf("a.go left changed in the worktree: %q", st)
		}
	})

	t.Run("workdir tree", func(t *testing.T) {
		t.Parallel()
		dir, wd := workdirRepo(t)
		write(t, wd, "package a\n")
		if _, err := Run(dir, RunOptions{Task: "T1", Workdir: wd}); err != nil {
			t.Fatalf("Run(Workdir) error = %v", err)
		}
		write(t, wd, "package a // c1\n")
		if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: writeDeltaFile(t)}); err != nil {
			t.Fatalf("Run() delta error = %v", err)
		}
		fin := lastFinished(t, dir, "T1")
		if fin.Attempt != "c1" || fin.Commit != "" {
			t.Errorf("finished attempt=%q commit=%q, want c1 with no commit", fin.Attempt, fin.Commit)
		}
		if d := lastDispatched(t, dir, "T1"); !samePath(d.Workdir, wd) {
			t.Errorf("delta dispatched in %q, want %s", d.Workdir, wd)
		}
	})
}
