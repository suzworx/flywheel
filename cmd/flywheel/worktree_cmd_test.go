package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
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

// TestWorktreeCommitCmd checks `flywheel worktree commit` (issue #897): a
// missing task, a missing --session and an unknown subcommand are usage (2),
// an unknown task exits 1, and a task worktree whose owns were amended to a
// path the attempt commit left out commits it (0), naming what stays out.
func TestWorktreeCommitCmd(t *testing.T) {
	t.Parallel()
	r := t.TempDir()
	worktreeGit(t, r, "init", "-q")
	if err := os.WriteFile(filepath.Join(r, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktreeGit(t, r, "add", "-A")
	worktreeGit(t, r, "commit", "-q", "-m", "initial")
	wt := filepath.Join(r, ".flywheel", "worktrees", "T1")
	worktreeGit(t, r, "worktree", "add", "-q", "-b", "fw/T1", wt)
	h := &flywheel.BriefHeader{Owns: []string{"a.go"}}
	amended := &flywheel.BriefHeader{Owns: []string{"a.go", "b.go"}}
	for _, e := range []flywheel.Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt", Owns: h.Owns, Header: h},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt", Header: h},
		{Task: "T1", Kind: "finished", Attempt: "r1", Uncommitted: []string{"b.go"}},
		{Task: "T1", Kind: "amended", Brief: "brief.txt", Owns: amended.Owns, Header: amended},
	} {
		if err := flywheel.AppendEvent(r, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(wt, n), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		rc := worktreeMain(args, &out, &errb)
		return rc, out.String() + errb.String()
	}
	for name, args := range map[string][]string{
		"no task":     {"commit", "--session", "lead", "--dir", r},
		"no session":  {"commit", "T1", "--dir", r},
		"unknown sub": {"prune", "T1", "--session", "lead", "--dir", r},
	} {
		if rc, out := run(args...); rc != 2 {
			t.Errorf("%s: rc=%d out=%s, want 2", name, rc, out)
		}
	}
	if rc, out := run("commit", "T9", "--session", "lead", "--dir", r); rc != 1 {
		t.Errorf("unknown task: rc=%d out=%s, want 1", rc, out)
	}
	rc, out := run("commit", "--session", "lead", "--dir", r, "T1")
	if rc != 0 || !strings.Contains(out, "recommitted T1 r1: ") || strings.Contains(out, "nothing to commit") ||
		!strings.Contains(out, "still uncommitted (outside owns): c.go") {
		t.Fatalf("happy path: rc=%d out=%s, want 0, a commit and c.go still out", rc, out)
	}
	events, err := flywheel.ReadEvents(r)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != "recommitted" || last.Session != "lead" || last.Commit == "" {
		t.Errorf("last event = %+v, want recommitted by lead with a commit", last)
	}
}
