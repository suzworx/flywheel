package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCheckpointUnclean checks an attempt that ends with a provider error
// after writing an owned file is checkpointed (issue #422): the finished event
// names the checkpoint, refs/flywheel/checkpoints/T1/r1 points at a commit of
// the owned file only, and neither fw/T1 nor the worktree's index moved.
func TestCheckpointUnclean(t *testing.T) {
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	before := strings.TrimSpace(git(t, wt, []string{"rev-parse", "HEAD"}))
	for name, body := range map[string]string{"a.go": "package a // partial\n", "notes.txt": "not owned\n"} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(t.TempDir(), "write-then-error.jsonl")
	lines := `{"type":"step_start","sessionID":"ses_cp","part":{"type":"step_start","step":1}}
{"type":"tool_use","sessionID":"ses_cp","part":{"type":"tool_use","tool":"write","state":{"input":{"filePath":"a.go"}}}}
{"type":"step_finish","sessionID":"ses_cp","part":{"type":"step_finish","reason":"tool-calls","tokens":{"input":1,"output":1}}}
{"type":"error","sessionID":"ses_cp","error":{"data":{"message":"HTTP 500"}}}
`
	if err := os.WriteFile(fixture, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, simConfig(fixture)); err != nil {
		t.Fatal(err)
	}
	res, err := Run(dir, RunOptions{Task: "T1", Worktree: true})
	if err != nil || res.Reason != "error" {
		t.Fatalf("Run() = %+v, %v; want reason error", res, err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fin Event
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "finished" {
			fin = e
		}
	}
	if fin.Checkpoint == "" {
		t.Fatalf("finished = %+v, want a checkpoint", fin)
	}
	if got := strings.TrimSpace(git(t, dir, []string{"rev-parse", "refs/flywheel/checkpoints/T1/r1"})); got != fin.Checkpoint {
		t.Errorf("checkpoint ref = %s, finished.checkpoint = %s", got, fin.Checkpoint)
	}
	if files := strings.TrimSpace(git(t, dir, []string{"show", "--name-only", "--format=%s", fin.Checkpoint})); files != "checkpoint T1 r1\n\na.go" {
		t.Errorf("checkpoint commit = %q, want message and a.go only", files)
	}
	if head := strings.TrimSpace(git(t, wt, []string{"rev-parse", "refs/heads/fw/T1"})); head != before {
		t.Errorf("fw/T1 moved to %s from %s", head, before)
	}
	if staged := strings.TrimSpace(git(t, wt, []string{"diff", "--cached", "--name-only"})); staged != "" {
		t.Errorf("the worktree index changed: %q staged", staged)
	}
	cps, err := ListCheckpoints(dir, "T1")
	if err != nil || len(cps) != 1 || !slices.Equal(cps[0].Paths, []string{"a.go"}) {
		t.Errorf("ListCheckpoints = %+v, %v", cps, err)
	}
}

// TestCheckpointRestore checks restore refuses over uncommitted changes to
// the checkpoint's paths unless forced, writes the checkpoint's content back
// without staging it, and drop removes the ref (issue #422).
func TestCheckpointRestore(t *testing.T) {
	dir := worktreeRepo(t)
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(wt, "a.go")
	want := "package a // saved\n"
	if err := os.WriteFile(a, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpointAttempt(wt, "T1", "r1", []string{"a.go"}); err != nil {
		t.Fatalf("checkpointAttempt: %v", err)
	}
	if err := os.WriteFile(a, []byte("package a // clobbered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreCheckpoint(dir, "T1", "", false); err == nil || !strings.Contains(err.Error(), "a.go") {
		t.Fatalf("restore over a dirty a.go: err = %v, want a refusal naming a.go", err)
	}
	if d, err := DiffCheckpoint(dir, "T1", "r1"); err != nil || !strings.Contains(d, "clobbered") {
		t.Errorf("DiffCheckpoint = %q, %v; want the clobbered line", d, err)
	}
	paths, err := RestoreCheckpoint(dir, "T1", "r1", true)
	if err != nil || !slices.Equal(paths, []string{"a.go"}) {
		t.Fatalf("forced restore = %v, %v", paths, err)
	}
	if b, _ := os.ReadFile(a); strings.ReplaceAll(string(b), "\r\n", "\n") != want {
		t.Errorf("a.go = %q, want %q", b, want)
	}
	if staged := strings.TrimSpace(git(t, wt, []string{"diff", "--cached", "--name-only"})); staged != "" {
		t.Errorf("restore staged %q", staged)
	}
	git(t, wt, []string{"checkout", "--", "a.go"})
	if _, err := RestoreCheckpoint(dir, "T1", "r1", false); err != nil {
		t.Errorf("restore over a clean a.go: %v", err)
	}
	if err := DropCheckpoint(dir, "T1", "r1"); err != nil {
		t.Fatal(err)
	}
	if cps, err := ListCheckpoints(dir, "T1"); err != nil || len(cps) != 0 {
		t.Errorf("after drop: %+v, %v", cps, err)
	}
}
