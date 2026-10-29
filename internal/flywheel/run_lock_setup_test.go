package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// blockingSetupRepo is a worktreeRepo (T1 planned, owns a.go) whose
// worktree.setup blocks T1's setup until a release file appears in T1's
// worktree, after writing a started file there; every other task's setup
// returns at once. The poll gives up after two minutes, so a test that never
// releases cannot hang the suite.
func blockingSetupRepo(t *testing.T) (dir, wt string) {
	t.Helper()
	dir = setupRepo(t, `if [ "$FLYWHEEL_TASK" = T1 ]; then : > setup-started; n=0; until [ -f setup-release ]; do n=$((n+1)); [ $n -le 1200 ] || exit 7; sleep 0.1; done; fi`)
	return dir, filepath.Join(dir, ".flywheel", "worktrees", "T1")
}

// waitForFile polls for p until it exists or the deadline passes.
func waitForFile(t *testing.T, p string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(p); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear within %s", p, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startBlockedT1 starts Run(T1, --worktree) in a goroutine and returns once
// its worktree.setup has started; the channel delivers Run's outcome.
func startBlockedT1(t *testing.T, dir, wt string) <-chan concurrentOutcome {
	t.Helper()
	done := make(chan concurrentOutcome, 1)
	go func() {
		res, err := Run(dir, RunOptions{Task: "T1", Worktree: true})
		done <- concurrentOutcome{res, err}
	}()
	waitForFile(t, filepath.Join(wt, "setup-started"), 2*time.Minute)
	return done
}

// releaseT1 lets T1's setup finish and returns Run's outcome.
func releaseT1(t *testing.T, wt string, done <-chan concurrentOutcome) concurrentOutcome {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, "setup-release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		return o
	case <-time.After(3 * time.Minute):
		t.Fatal("Run(T1) did not return within 3m of releasing its setup")
		return concurrentOutcome{}
	}
}

// dispatchedFor counts task's dispatched events.
func dispatchedFor(t *testing.T, dir, task string) int {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Task == task && e.Kind == "dispatched" {
			n++
		}
	}
	return n
}

// TestRunDispatchLockNotHeldThroughSetup checks the dispatch lock is released
// before worktree creation and worktree.setup and taken again only for the
// dispatched append (issue #893): while T1's setup blocks, dispatch.lock does
// not exist and a disjoint T2 dispatches; a T3 overlapping T1's owns that is
// dispatched meanwhile makes T1's second phase refuse on owns.
func TestRunDispatchLockNotHeldThroughSetup(t *testing.T) {
	t.Parallel()
	t.Run("disjoint dispatch during setup", func(t *testing.T) {
		t.Parallel()
		dir, wt := blockingSetupRepo(t)
		planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go"))
		done := startBlockedT1(t, dir, wt)
		if _, err := os.Stat(filepath.Join(dir, ".flywheel", "dispatch.lock")); !os.IsNotExist(err) {
			t.Errorf("dispatch.lock exists while T1's worktree.setup runs (stat err %v), want it released", err)
		}
		short := defaultRepoLockTimings()
		short.wait = 5 * time.Second
		res, err := Run(dir, RunOptions{Task: "T2", Worktree: true, lockTimings: &short})
		if err != nil {
			t.Errorf("Run(T2) error = %v while T1's setup was blocked, want a dispatch", err)
		} else if res.Attempt != "r1" || res.Reason != "stop" {
			t.Errorf("Run(T2) = %+v, want a clean r1 run", res)
		}
		select {
		case o := <-done:
			t.Fatalf("Run(T1) returned %+v, %v before its setup was released", o.res, o.err)
		default:
		}
		o := releaseT1(t, wt, done)
		if o.err != nil || o.res.Attempt != "r1" || o.res.Reason != "stop" {
			t.Errorf("Run(T1) = %+v, %v, want a clean r1 run once setup finished", o.res, o.err)
		}
		if n := dispatchedFor(t, dir, "T1"); n != 1 {
			t.Errorf("T1 dispatched events = %d, want 1", n)
		}
	})
	t.Run("overlap dispatched during setup refuses in phase B", func(t *testing.T) {
		t.Parallel()
		dir, wt := blockingSetupRepo(t)
		done := startBlockedT1(t, dir, wt)
		planAndDispatch(t, dir, "T3", ownsBrief(t, dir, "b3.txt", "a.go"))
		o := releaseT1(t, wt, done)
		var rf *RuleRefusal
		if !errors.As(o.err, &rf) || rf.Rule != "owns" {
			t.Fatalf("Run(T1) error = %v, want the owns RuleRefusal from the phase-B re-check", o.err)
		}
		if n := dispatchedFor(t, dir, "T1"); n != 0 {
			t.Errorf("T1 dispatched events = %d, want 0 after the phase-B refusal", n)
		}
		evs, err := ReadEvents(dir)
		if err != nil {
			t.Fatal(err)
		}
		refused := false
		for _, e := range evs {
			refused = refused || e.Task == "T1" && e.Kind == "dispatch_refused" && e.Rule == "owns"
		}
		if !refused {
			t.Error("no dispatch_refused owns event for T1, want the phase-B refusal recorded")
		}
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("T1's prepared worktree is gone after the phase-B refusal (%v), want it left in place", err)
		}
	})
}
