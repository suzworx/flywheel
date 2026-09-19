package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitWriteStateDescribesHead checks gitHistoryState of a repo with one
// commit contains ok=true and the expected format with HEAD, branch and stash.
func TestGitWriteStateDescribesHead(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create initial commit.
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	state, ok := gitHistoryState(dir)
	if !ok {
		t.Fatalf("gitHistoryState() ok = false, want true")
	}
	if !strings.Contains(state, "HEAD=") {
		t.Errorf("state = %q, want to contain HEAD=", state)
	}
	if !strings.Contains(state, "branch=refs/heads/") {
		t.Errorf("state = %q, want to contain branch=refs/heads/", state)
	}
	if !strings.Contains(state, "stash=(none)") {
		t.Errorf("state = %q, want to contain stash=(none)", state)
	}
}

// TestGitWriteStateNotARepo checks gitHistoryState of a plain temp dir returns
// ok=false when GIT_CEILING_DIRECTORIES prevents git from finding a repo.
func TestGitWriteStateNotARepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	state, ok := gitHistoryState(dir)
	if ok {
		t.Fatalf("gitHistoryState() ok = true, want false; state = %q", state)
	}
	if state != "" {
		t.Errorf("state = %q, want empty", state)
	}
}

// TestGitWriteCommitDuringRunRecordsSignal checks that when commandHook makes
// an empty commit during Run, a git-write signal is recorded and the finished
// event's note mentions the change.
func TestGitWriteCommitDuringRunRecordsSignal(t *testing.T) {
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Hook that makes a commit during the run.
	commandHook = func(r RunRequest) {
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "worker-commit")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	defer func() { commandHook = nil }()

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	hasGitWriteSignal := false
	hasNoteChange := false
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			hasGitWriteSignal = true
		}
		if e.Kind == "finished" && e.Task == "T1" && strings.Contains(e.Note, "git history changed") {
			hasNoteChange = true
		}
	}

	if !hasGitWriteSignal {
		t.Errorf("no git-write signal found for T1")
	}
	if !hasNoteChange {
		t.Errorf("finished event note does not mention git history changed")
	}
}

// TestGitWriteCleanRunRecordsNoSignal checks that when no hook runs and git
// history is unchanged, no git-write signal is recorded.
func TestGitWriteCleanRunRecordsNoSignal(t *testing.T) {
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			t.Errorf("unexpected git-write signal found for T1")
		}
	}
}

// TestGitWriteStashDuringRunRecordsSignal checks that when commandHook runs
// git stash, a git-write signal is recorded.
func TestGitWriteStashDuringRunRecordsSignal(t *testing.T) {
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	// Write and commit a tracked file so we can stash it.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked content\n"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	cmd = exec.Command("git", "add", "tracked.txt")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "add tracked")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit tracked: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Hook that modifies the file and stashes it.
	commandHook = func(r RunRequest) {
		if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified content\n"), 0o644); err != nil {
			return
		}
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "stash")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	defer func() { commandHook = nil }()

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	hasGitWriteSignal := false
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			hasGitWriteSignal = true
		}
	}

	if !hasGitWriteSignal {
		t.Errorf("no git-write signal found for T1 after stash")
	}
}
