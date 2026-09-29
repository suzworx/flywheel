package flywheel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// refusedTask is a planned T1 with brief text and the sim worker configured.
func refusedTask(t *testing.T, text string) string {
	t.Helper()
	dir := needsEnvTask(t, text)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	return dir
}

// TestRefusedPreflightOutsideDispatchLock: while a preflight command runs, a
// second command takes the dispatch lock with a short wait and gets it, so
// preflight never holds the lock (issue #651). Not parallel: it swaps
// runPreflight.
func TestRefusedPreflightOutsideDispatchLock(t *testing.T) {
	dir := refusedTask(t, "preflight: slow-check\n\n# TASK x\n")
	orig := runPreflight
	t.Cleanup(func() { runPreflight = orig })
	var lockErr error
	runPreflight = func(wd, cmd string) (int, int64, []byte, error) {
		release, err := acquireRepoLock(wd, "dispatch.lock", repoLockTimings{staleAfter: 15 * time.Second, wait: 500 * time.Millisecond, retry: time.Millisecond, heartbeat: time.Second, holder: "test"})
		if lockErr = err; err != nil {
			return 1, 0, []byte(err.Error()), nil
		}
		release()
		return 0, 0, nil, nil
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if lockErr != nil {
		t.Fatalf("dispatch lock taken during preflight: error = %v, want it free", lockErr)
	}
	if evs, _ := ReadEvents(dir); !hasEventKind(evs, "dispatched") {
		t.Fatal("no dispatched event recorded")
	}
}

// TestRefusedDispatchLockRecorded: a dispatch that times out on the dispatch
// lock records dispatch_refused rule dispatch-lock naming the holder, and
// Derive shows it as Refused on the still-planned unit (issue #651).
func TestRefusedDispatchLockRecorded(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "# TASK x\n")
	release, err := acquireRepoLock(dir, "dispatch.lock", repoLockTimings{staleAfter: 15 * time.Second, wait: time.Second, retry: time.Millisecond, heartbeat: time.Second, holder: "run o12"})
	if err != nil {
		t.Fatalf("acquire holder error = %v", err)
	}
	defer release()
	short := repoLockTimings{staleAfter: 15 * time.Second, wait: 100 * time.Millisecond, retry: 5 * time.Millisecond, heartbeat: time.Second}
	_, err = Run(dir, RunOptions{Task: "T1", lockTimings: &short})
	var busy *RepoLockBusy
	if !errors.As(err, &busy) {
		t.Fatalf("Run() error = %v, want a RepoLockBusy", err)
	}
	refused := kindEvents(t, dir, "dispatch_refused")
	if len(refused) != 1 || refused[0].Rule != "dispatch-lock" || !strings.Contains(refused[0].Note, "held by run o12 (pid ") {
		t.Fatalf("dispatch_refused events = %+v, want one rule dispatch-lock naming run o12", refused)
	}
	evs, _ := ReadEvents(dir)
	ts := Derive(evs).Tasks[0]
	if ts.Status != "planned" || !strings.HasPrefix(ts.Refused, "dispatch-lock: lock ") {
		t.Errorf("task = %s refused %q, want planned refused dispatch-lock", ts.Status, ts.Refused)
	}
}

// TestRefusedPreflightRecordedOnce: a preflight refusal records
// dispatch_refused rule preflight, the same refusal again records nothing
// more, and neither a resume with no session nor an unplanned task's refusal
// is recorded (issue #651).
func TestRefusedPreflightRecordedOnce(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "preflight: exit 3\n\n# TASK x\n")
	for i := 0; i < 2; i++ {
		var rf *RuleRefusal
		if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "preflight" {
			t.Fatalf("Run() #%d error = %v, want a preflight refusal", i+1, err)
		}
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Resume: true}); !IsNoWorkerSession(err) {
		t.Fatalf("Run(--resume) error = %v, want NoWorkerSession", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T9", Base: "main"}); err == nil {
		t.Fatal("Run(T9 --base without --worktree) succeeded, want a refusal")
	}
	refused := kindEvents(t, dir, "dispatch_refused")
	if len(refused) != 1 || refused[0].Task != "T1" || refused[0].Rule != "preflight" || !strings.Contains(refused[0].Note, "exit 3") {
		t.Fatalf("dispatch_refused events = %+v, want exactly one T1 preflight refusal", refused)
	}
}

// wantRefusedAppended fails t unless the ledger in dir holds exactly one event
// more than before, a dispatch_refused for task with rule (issue #651).
func wantRefusedAppended(t *testing.T, dir string, before []Event, task, rule string) {
	t.Helper()
	after, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("refusal appended %d event(s), want one dispatch_refused", len(after)-len(before))
	}
	if e := after[len(after)-1]; e.Kind != "dispatch_refused" || e.Task != task || e.Rule != rule {
		t.Errorf("appended %s task %s rule %q, want dispatch_refused task %s rule %q", e.Kind, e.Task, e.Rule, task, rule)
	}
}

// TestRefusedSuspendedNotRecorded: a Run refused because the factory is
// suspended appends nothing, even twice: the owner froze the ledger (issue
// #651).
func TestRefusedSuspendedNotRecorded(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "# TASK x\n")
	if err := Suspend(dir, "lead", "freeze", time.Time{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var rf *RuleRefusal
		if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "suspended" {
			t.Fatalf("Run() #%d error = %v, want a suspended refusal", i+1, err)
		}
	}
	if refused := kindEvents(t, dir, "dispatch_refused"); len(refused) != 0 {
		t.Errorf("dispatch_refused events = %+v, want none while suspended", refused)
	}
}

// TestRefusedDerive: Derive sets Refused from the newest dispatch_refused and
// leaves the status alone; a later dispatched clears it (issue #651).
func TestRefusedDerive(t *testing.T) {
	t.Parallel()
	evs := []Event{
		{TS: "2026-09-27T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-27T00:01:00Z", Task: "T1", Kind: "dispatch_refused", Rule: "preflight", Note: "old"},
		{TS: "2026-09-27T00:02:00Z", Task: "T1", Kind: "dispatch_refused", Rule: "dispatch-lock", Note: "held"},
	}
	if ts := Derive(evs).Tasks[0]; ts.Status != "planned" || ts.Refused != "dispatch-lock: held" {
		t.Fatalf("task = %s refused %q, want planned refused %q", ts.Status, ts.Refused, "dispatch-lock: held")
	}
	evs = append(evs, Event{TS: "2026-09-27T00:03:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"})
	if ts := Derive(evs).Tasks[0]; ts.Status != "dispatched" || ts.Refused != "" {
		t.Errorf("task = %s refused %q, want dispatched with no refusal", ts.Status, ts.Refused)
	}
	if err := Validate(Event{Kind: "dispatch_refused", Task: "T1"}); err == nil {
		t.Error("Validate(dispatch_refused without rule) = nil, want an error")
	}
}
