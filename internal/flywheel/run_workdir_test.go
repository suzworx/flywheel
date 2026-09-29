package flywheel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// workdirRepo is a worktreeRepo plus a second working tree of the same
// repository on branch side, outside dir (issue #545).
func workdirRepo(t *testing.T) (dir, wt string) {
	t.Helper()
	dir = worktreeRepo(t)
	wt = filepath.Join(t.TempDir(), "wt2")
	git(t, dir, []string{"worktree", "add", "-q", "-b", "side", wt})
	return dir, wt
}

// TestRunWorkdirDispatches checks run --workdir starts the worker in the
// given tree while the dispatched event, recording that workdir, goes to
// dir's ledger and the tree's own .flywheel/ gets no events.jsonl.
func TestRunWorkdirDispatches(t *testing.T) {
	// not parallel: t.Setenv PATH to a fake claude
	dir, wt := workdirRepo(t)
	if err := WriteConfig(dir, Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "claude")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := linkOrCopy(exe, fake); err != nil {
		t.Fatalf("install fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stream := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(stream, []byte(`{"type":"system","subtype":"init","session_id":"ses_wd"}`+"\n"+
		`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":"ses_wd","total_cost_usd":0.01}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeEnv, stream)
	// A relative path: the fake claude saves its stdin in its own cwd.
	t.Setenv(fakeClaudeStdinEnv, "worker-cwd.marker")
	if _, err := Run(dir, RunOptions{Task: "T1", Workdir: wt}); err != nil {
		t.Fatalf("Run(Workdir) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "worker-cwd.marker")); err != nil {
		t.Errorf("worker did not run in the workdir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "worker-cwd.marker")); err == nil {
		t.Error("worker ran in dir, want the workdir")
	}
	if d := lastDispatched(t, dir, "T1"); d.Workdir == "" || !samePath(d.Workdir, wt) {
		t.Errorf("dispatched.Workdir = %q, want %s", d.Workdir, wt)
	}
	if _, err := os.Stat(filepath.Join(wt, ".flywheel", "events.jsonl")); err == nil {
		t.Error("the workdir's .flywheel/ has an events.jsonl; events belong in dir's ledger")
	}
	// A delta with no --workdir continues in the recorded workdir.
	delta := filepath.Join(t.TempDir(), "delta.txt")
	if err := os.WriteFile(delta, []byte("owns: a.go\nneeds: none\n\n# TASK: fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta, Progress: &buf}); err != nil {
		t.Fatalf("Run(delta) error = %v", err)
	}
	if !strings.Contains(buf.String(), "T1 resuming in workdir ") {
		t.Errorf("progress = %q, want a resuming in workdir line", buf.String())
	}
	if d := lastDispatched(t, dir, "T1"); d.Attempt == "r1" || !samePath(d.Workdir, wt) {
		t.Errorf("delta dispatched = %s in %q, want a new attempt in %s", d.Attempt, d.Workdir, wt)
	}
}

// TestRunWorkdirRefusals checks each bad --workdir is a RuleRefusal before
// dispatched that appends only its dispatch_refused (issue #651).
func TestRunWorkdirRefusals(t *testing.T) {
	t.Parallel()
	dir, wt := workdirRepo(t)
	other := worktreeRepo(t)
	otherWt := filepath.Join(t.TempDir(), "other-wt")
	git(t, other, []string{"worktree", "add", "-q", "-b", "o", otherWt})
	for _, tc := range []struct {
		name, rule string
		o          RunOptions
	}{
		{"with worktree", "workdir", RunOptions{Task: "T1", Workdir: wt, Worktree: true}},
		{"with base", "base", RunOptions{Task: "T1", Workdir: wt, Base: "HEAD"}},
		{"missing", "workdir", RunOptions{Task: "T1", Workdir: filepath.Join(t.TempDir(), "nope")}},
		{"not a repo", "workdir", RunOptions{Task: "T1", Workdir: t.TempDir()}},
		{"other repo", "workdir", RunOptions{Task: "T1", Workdir: otherWt}},
	} {
		before := mustEvents(t, dir)
		_, err := Run(dir, tc.o)
		var r *RuleRefusal
		if !errors.As(err, &r) || r.Rule != tc.rule {
			t.Errorf("%s: Run() error = %v, want a RuleRefusal %s", tc.name, err, tc.rule)
		}
		wantRefusedAppended(t, dir, before, "T1", tc.rule)
	}
}

// TestRunWorkdirBaselinesMerge checks a conflicted merge in progress in the
// workdir is baselined at dispatch, so the owns check never blames the worker.
func TestRunWorkdirBaselinesMerge(t *testing.T) {
	t.Parallel()
	dir, wt := workdirRepo(t)
	main := git(t, dir, []string{"rev-parse", "--abbrev-ref", "HEAD"})
	for _, c := range []struct{ tree, text string }{{dir, "main\n"}, {wt, "side\n"}} {
		if err := os.WriteFile(filepath.Join(c.tree, "a.txt"), []byte(c.text), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, c.tree, []string{"add", "a.txt"})
		git(t, c.tree, []string{"commit", "-q", "-m", "a " + c.text})
	}
	merge := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false", "merge", main)
	merge.Dir = wt
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge did not conflict: %s", out)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Workdir: wt}); err != nil {
		t.Fatalf("Run(Workdir) error = %v", err)
	}
	if d := lastDispatched(t, dir, "T1"); d.Baseline["a.txt"] == "" {
		t.Errorf("dispatched.Baseline = %v, want a.txt", fmt.Sprint(d.Baseline))
	}
}
