package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCheckpointUnclean checks an attempt that ends with a provider error
// after writing an owned file is checkpointed (issue #422): the finished event
// names the checkpoint, refs/flywheel/checkpoints/T1/r1 points at a commit of
// the owned file only, and neither fw/T1 nor the worktree's index moved.
func TestCheckpointUnclean(t *testing.T) {
	t.Parallel()
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

// timedCheckpointRun starts a clean-stopping sim Run of T1 in its worktree
// with limits.checkpoint_every set to every and the sim held for simDelay, or,
// with hold, until release is called (issue #619); a.go is written before
// dispatch. It returns the flywheel dir, the worktree, the tick counter, a
// channel closed when Run returns and release (a no-op without hold).
func timedCheckpointRun(t *testing.T, every string, simDelay time.Duration, hold bool) (dir, wt string, ticks *atomic.Int64, done chan struct{}, release func()) {
	t.Helper()
	dir = worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "stop.jsonl")
	lines := `{"type":"step_start","sessionID":"ses_tc","part":{"type":"step_start","step":1}}
{"type":"step_finish","sessionID":"ses_tc","part":{"type":"step_finish","reason":"stop","tokens":{"input":1,"output":1}}}
`
	if err := os.WriteFile(fixture, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := simConfig(fixture)
	cfg.Limits.CheckpointEvery = every
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	ticks = new(atomic.Int64)
	done = make(chan struct{})
	t.Cleanup(func() { <-done }) // a failed test still waits for Run before its temp dirs go
	var gate chan struct{}
	release = func() {}
	if hold {
		gate = make(chan struct{})
		var once sync.Once
		release = func() { once.Do(func() { close(gate) }) }
		// Registered after the wait on done, so it runs first (last in, first
		// out): a test that failed mid-way still lets Run finish.
		t.Cleanup(release)
	}
	go func() {
		defer close(done)
		o := RunOptions{Task: "T1", Worktree: true, SimDelay: simDelay, checkpointTicks: ticks}
		if gate != nil {
			o.simRelease = gate
		}
		if res, err := Run(dir, o); err != nil || res.Reason != "stop" {
			t.Errorf("Run() = %+v, %v; want reason stop", res, err)
		}
	}()
	return dir, wt, ticks, done, release
}

// waitMidRun polls cond until it holds; it fails when Run finished first (the
// condition must hold mid-run) or after a hang guard.
func waitMidRun(t *testing.T, done chan struct{}, what string, cond func() bool) {
	t.Helper()
	for guard := time.Now().Add(60 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		select {
		case <-done:
			t.Fatalf("Run finished before %s", what)
		default:
		}
		if time.Now().After(guard) {
			t.Fatalf("hang guard: never saw %s", what)
		}
	}
}

// TestTimedCheckpoint checks a worktree attempt is checkpointed on the
// limits.checkpoint_every timer while it runs (issue #528): the ref holds the
// owned file before the run finishes, a tick with no change writes no new
// commit, and a later change moves the ref.
func TestTimedCheckpoint(t *testing.T) {
	t.Parallel()
	dir, wt, ticks, done, release := timedCheckpointRun(t, "20ms", 0, true) // the sim holds until release; the waits are hang guards
	ref := checkpointRef("T1", "r1")
	refSHA := func() string { sha, _ := gitWith(dir, nil, "rev-parse", "--verify", "-q", ref); return sha }
	waitMidRun(t, done, "a timed checkpoint", func() bool { return refSHA() != "" })
	if body, err := gitWith(dir, nil, "show", ref+":a.go"); err != nil || body != "package a // one" {
		t.Fatalf("checkpoint a.go = %q, %v", body, err)
	}
	first, seen := refSHA(), time.Now()
	// A commit's date has one-second resolution: only a tick more than a
	// second later would write a different sha were the unchanged tree not
	// skipped.
	waitMidRun(t, done, "a second to pass", func() bool { return time.Since(seen) > 1100*time.Millisecond })
	n := ticks.Load()
	waitMidRun(t, done, "two more ticks", func() bool { return ticks.Load() >= n+2 })
	if got := refSHA(); got != first {
		t.Errorf("ref moved from %s to %s with no change", first, got)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitMidRun(t, done, "the change checkpointed", func() bool {
		body, _ := gitWith(dir, nil, "show", ref+":a.go")
		return body == "package a // two"
	})
	release()
	<-done
}

// TestTimedCheckpointDisabled checks checkpoint_every "0" takes no timed
// checkpoint: a clean run leaves no ref and the timer never ticked.
func TestTimedCheckpointDisabled(t *testing.T) {
	t.Parallel()
	dir, _, ticks, done, _ := timedCheckpointRun(t, "0", 500*time.Millisecond, false)
	<-done
	if sha, err := gitWith(dir, nil, "rev-parse", "--verify", "-q", checkpointRef("T1", "r1")); err == nil {
		t.Errorf("checkpoint ref = %s, want none with checkpoint_every 0", sha)
	}
	if n := ticks.Load(); n != 0 {
		t.Errorf("ticks = %d, want 0", n)
	}
}

// TestCheckpointRestore checks restore refuses over uncommitted changes to
// the checkpoint's paths unless forced, writes the checkpoint's content back
// without staging it, and drop removes the ref (issue #422).
func TestCheckpointRestore(t *testing.T) {
	t.Parallel()
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
