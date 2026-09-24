package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitOut runs git in dir and returns its trimmed stdout, failing the test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestUnitCommit checks commitAttempt (#391): an owned change is committed on
// fw/<task> with the Flywheel-Task trailer and left clean in git status; an
// unowned change stays uncommitted and is reported; bookkeeping is neither
// committed nor reported; a directory that is not a task worktree is untouched.
func TestUnitCommit(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(dir, ".flywheel", "worktrees", "T")
	gitOut(t, dir, "worktree", "add", "-q", wt, "-b", "fw/T")
	before := gitOut(t, wt, "rev-parse", "HEAD")

	for p, body := range map[string]string{
		"src/a.go":          "package a\n",
		"notes.txt":         "not mine\n",
		".flywheel/scratch": "bookkeeping\n",
	} {
		full := filepath.Join(wt, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sha, outside, err := commitAttempt(dir, wt, "T", "a1", []string{"src/"})
	if err != nil {
		t.Fatalf("commitAttempt: %v", err)
	}
	if sha == "" {
		t.Fatal("no commit for an owned change")
	}
	if !slices.Equal(outside, []string{"notes.txt"}) {
		t.Errorf("outside = %v, want [notes.txt]", outside)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/heads/fw/T"); got != sha {
		t.Errorf("fw/T = %s, want %s", got, sha)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD^"); got != before {
		t.Errorf("parent = %s, want %s", got, before)
	}
	msg := gitOut(t, wt, "log", "-1", "--format=%B")
	if !strings.HasPrefix(msg, "T a1") || !strings.Contains(msg, "Flywheel-Task: T") {
		t.Errorf("message = %q", msg)
	}
	if who := gitOut(t, wt, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); who != "flywheel <flywheel@localhost>|flywheel <flywheel@localhost>" {
		t.Errorf("author|committer = %q", who)
	}
	if files := gitOut(t, wt, "show", "--name-only", "--format=", "HEAD"); files != "src/a.go" {
		t.Errorf("committed files = %q, want src/a.go", files)
	}
	if st := gitOut(t, wt, "status", "--porcelain", "--", "src"); st != "" {
		t.Errorf("owned path not clean after commit: %q", st)
	}
	if st := gitOut(t, wt, "status", "--porcelain", "--", "notes.txt"); st != "?? notes.txt" {
		t.Errorf("unowned path status = %q, want untracked", st)
	}

	// Nothing owned left to commit: no commit, the unowned path still reported.
	sha, outside, err = commitAttempt(dir, wt, "T", "a2", []string{"src/"})
	if err != nil || sha != "" || !slices.Equal(outside, []string{"notes.txt"}) {
		t.Errorf("second commitAttempt = %q, %v, %v; want no commit", sha, outside, err)
	}

	// The repository root is not a task worktree: nothing happens.
	if err := os.WriteFile(filepath.Join(dir, "root.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, dir, "rev-parse", "HEAD")
	sha, outside, err = commitAttempt(dir, dir, "T", "a1", []string{"root.go"})
	if sha != "" || outside != nil || err != nil {
		t.Errorf("non-task worktree: %q, %v, %v; want nothing", sha, outside, err)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("root HEAD moved: %s -> %s", head, got)
	}
}
