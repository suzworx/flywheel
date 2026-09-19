package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeCreatesBranchAndDir tests that TaskWorktree creates the worktree
// directory and branch on first call.
func TestWorktreeCreatesBranchAndDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatalf("TaskWorktree() error = %v", err)
	}

	// Check that the path is correct
	expected := filepath.Join(dir, ".flywheel", "worktrees", "T1")
	if wt != expected {
		t.Errorf("TaskWorktree() path = %q, want %q", wt, expected)
	}

	// Check that the directory exists
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree directory does not exist: %v", err)
	}

	// Check that the branch is fw/T1
	cmd := exec.Command("git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch != "fw/T1" {
		t.Errorf("branch = %q, want fw/T1", branch)
	}
}

// TestWorktreeReused tests that a second call to TaskWorktree returns the
// same path without error.
func TestWorktreeReused(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	wt1, err := TaskWorktree(dir, "T2")
	if err != nil {
		t.Fatalf("first TaskWorktree() error = %v", err)
	}

	wt2, err := TaskWorktree(dir, "T2")
	if err != nil {
		t.Fatalf("second TaskWorktree() error = %v", err)
	}

	if wt1 != wt2 {
		t.Errorf("second call returned different path: %q vs %q", wt1, wt2)
	}
}

// TestWorktreeRunRecordsWorkdir tests that Run records the worktree as Workdir
// on the dispatched event when --worktree is set.
func TestWorktreeRunRecordsWorkdir(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	brief := "owns: a.go\nneeds: none\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	_, err := Run(dir, RunOptions{Task: "T1", Worktree: true, Progress: os.Stderr, Stderr: os.Stderr})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	// Find the dispatched event
	var dispatched *Event
	for i := range events {
		if events[i].Task == "T1" && events[i].Kind == "dispatched" {
			dispatched = &events[i]
			break
		}
	}
	if dispatched == nil {
		t.Fatalf("no dispatched event found")
	}

	// Check that Workdir is set
	if dispatched.Workdir == "" {
		t.Errorf("dispatched event Workdir is empty")
	}

	// Check that it points to the worktree (normalize path separators for Windows)
	normalizedWorkdir := strings.ReplaceAll(dispatched.Workdir, "\\", "/")
	if !strings.Contains(normalizedWorkdir, ".flywheel/worktrees/T1") {
		t.Errorf("dispatched event Workdir = %q, does not contain .flywheel/worktrees/T1", dispatched.Workdir)
	}
}

// TestWorktreeValidateDefaultsToRecorded tests that ValidateTask uses the
// recorded worktree when Workdir is not specified.
func TestWorktreeValidateDefaultsToRecorded(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	brief := "owns: a.go\nneeds: none\ngate: go build ./...\n\n# TASK: test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})

	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	_, err := Run(dir, RunOptions{Task: "T1", Worktree: true, Progress: os.Stderr, Stderr: os.Stderr})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Now validate without specifying a workdir - it should use the recorded one
	result, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}

	// Check that we got a result (gate ran)
	if len(result.Gates) == 0 {
		t.Fatalf("ValidateTask() gates is empty, expected at least one gate")
	}

	// Look for the validated event in the event log to verify it used the right workdir
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	for _, e := range events {
		if e.Task == "T1" && e.Kind == "validated" {
			// The validated event should have the worktree recorded in Workdir (normalize for Windows)
			normalizedWorkdir := strings.ReplaceAll(e.Workdir, "\\", "/")
			if !strings.Contains(normalizedWorkdir, ".flywheel/worktrees/T1") && e.Workdir != "" {
				t.Errorf("validated event Workdir = %q, expected to contain .flywheel/worktrees/T1 or be empty for same tree", e.Workdir)
			}
			return
		}
	}
	t.Fatalf("no validated event found")
}

// TestWorktreeGitignored tests that .flywheel/.gitignore contains "worktrees/".
func TestWorktreeGitignored(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	gitignorePath := filepath.Join(dir, ".flywheel", ".gitignore")
	b, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}

	content := string(b)
	if !strings.Contains(content, "worktrees/") {
		t.Errorf(".gitignore does not contain 'worktrees/', got: %q", content)
	}
}

// worktreeRepo is a flywheel dir with a git repo, a committed brief planned
// as T1 (owns a.go) and the sim worker, ready for Run with Worktree.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: a.go\nneeds: none\n\n# TASK: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestWorktreeCleanRunNoFalseGitWrite checks that a --worktree run that
// touched no git history records no git-write signal: the before and after
// snapshots both read the task's worktree (#333 review).
func TestWorktreeCleanRunNoFalseGitWrite(t *testing.T) {
	dir := worktreeRepo(t)
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" {
			t.Fatalf("git-write signal on a clean --worktree run: %+v", e)
		}
		if e.Kind == "dispatched" && e.Task == "T1" {
			for path := range e.Worktrees {
				if strings.Contains(filepath.ToSlash(path), ".flywheel/worktrees/T1") {
					t.Errorf("the task's own worktree %s is snapshotted as another checkout", path)
				}
			}
		}
	}
}

// TestWorktreeRefusesForeignDir checks that a plain directory left at the
// worktree path is refused, not reused (#333 review).
func TestWorktreeRefusesForeignDir(t *testing.T) {
	dir := worktreeRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "worktrees", "T1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := TaskWorktree(dir, "T1"); err == nil {
		t.Error("TaskWorktree reused a plain directory, want an error")
	}
}
