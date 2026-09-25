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

// rebaseGit runs git in dir with a fixed identity, failing the test on error.
func rebaseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@e.x", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRebaseUnitCommand checks `flywheel rebase` (issue #414): usage errors
// exit 2; a unit with no task worktree exits 1; a unit stacked on a squashed
// base rebases onto main and exits 0.
func TestRebaseUnitCommand(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{}, {"A", "B"}, {"A", "--bogus"}} {
		if rc := rebaseMain(args, &out, &errOut); rc != 2 {
			t.Errorf("rebaseMain(%v) = %d, want 2", args, rc)
		}
	}

	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	rebaseGit(t, dir, "init", "-q", "-b", "main")
	write := func(d, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, name), []byte("package x // "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(dir, "a.go")
	rebaseGit(t, dir, "add", "-A")
	rebaseGit(t, dir, "commit", "-q", "-m", "init")
	rebaseGit(t, dir, "checkout", "-q", "-b", "fw/A")
	write(dir, "a1.go")
	rebaseGit(t, dir, "add", "a1.go")
	rebaseGit(t, dir, "commit", "-q", "-m", "A", "-m", "Flywheel-Task: A")
	base := rebaseGit(t, dir, "rev-parse", "HEAD")
	rebaseGit(t, dir, "checkout", "-q", "main")
	wt := filepath.Join(dir, ".flywheel", "worktrees", "B")
	rebaseGit(t, dir, "worktree", "add", "-q", "-b", "fw/B", wt, "fw/A")
	write(wt, "b.go")
	rebaseGit(t, wt, "add", "b.go")
	rebaseGit(t, wt, "commit", "-q", "-m", "B", "-m", "Flywheel-Task: B")
	rebaseGit(t, dir, "merge", "-q", "--squash", "fw/A")
	rebaseGit(t, dir, "commit", "-q", "-m", "A (#1)", "-m", "Flywheel-Task: A")
	squash := rebaseGit(t, dir, "rev-parse", "HEAD")
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "B", Kind: "dispatched", Attempt: "r1", Base: base, Workdir: wt}); err != nil {
		t.Fatal(err)
	}

	errOut.Reset()
	if rc := rebaseMain([]string{"A", "--dir", dir}, &out, &errOut); rc != 1 {
		t.Errorf("rebase of a unit with no task worktree = %d, want 1 (%s)", rc, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if rc := rebaseMain([]string{"B", "--dir", dir}, &out, &errOut); rc != 0 {
		t.Fatalf("rebase B = %d, want 0: %s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "B rebased onto "+squash) {
		t.Errorf("stdout = %q, want B rebased onto %s", out.String(), squash)
	}
	if parent := rebaseGit(t, dir, "rev-parse", "fw/B~1"); parent != squash {
		t.Errorf("fw/B~1 = %s, want %s", parent, squash)
	}
}
