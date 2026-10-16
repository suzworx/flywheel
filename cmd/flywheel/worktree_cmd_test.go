package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// worktreeGit runs git in dir with a fixed identity, failing the test on error.
func worktreeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestWorktreeRemoveCmd checks `flywheel worktree remove` (issue #851): a
// dirty tree exits 6 and stays, --discard without --session is usage (2), a
// path that is not a worktree exits 1, and a clean tree is removed (0).
func TestWorktreeRemoveCmd(t *testing.T) {
	t.Parallel()
	r := t.TempDir()
	worktreeGit(t, r, "init", "-q")
	worktreeGit(t, r, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(r, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeGit(t, r, "add", "-A")
	worktreeGit(t, r, "commit", "-q", "-m", "initial")
	wt := filepath.Join(t.TempDir(), "wt")
	worktreeGit(t, r, "worktree", "add", "-q", "-b", "wtb", wt)
	if err := os.WriteFile(filepath.Join(wt, "u.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		rc := worktreeMain(args, &out, &errb)
		return rc, out.String() + errb.String()
	}
	if rc, out := run("remove", wt, "--dir", r); rc != 6 || !strings.Contains(out, "u.txt") {
		t.Fatalf("dirty: rc=%d out=%s, want 6 naming u.txt", rc, out)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("dirty tree removed: %v", err)
	}
	if rc, out := run("remove", "--dir", r, "--discard", wt); rc != 2 {
		t.Fatalf("--discard without --session: rc=%d out=%s, want 2", rc, out)
	}
	if rc, out := run("remove", t.TempDir(), "--dir", r); rc != 1 {
		t.Fatalf("not a worktree: rc=%d out=%s, want 1", rc, out)
	}
	if err := os.Remove(filepath.Join(wt, "u.txt")); err != nil {
		t.Fatal(err)
	}
	if rc, out := run("remove", wt, "--dir", r); rc != 0 || !strings.Contains(out, "removed ") {
		t.Fatalf("clean: rc=%d out=%s, want 0 and removed", rc, out)
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
}
