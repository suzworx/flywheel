package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// wantSuspendedRefusal fails t unless err is the rule "suspended" refusal.
func wantSuspendedRefusal(t *testing.T, err error, what string) {
	t.Helper()
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "suspended" {
		t.Fatalf("%s = %v, want RuleRefusal suspended", what, err)
	}
}

// TestSuspendThawCycle: Suspend freezes with since/by/reason, a second Suspend
// is refused, Unsuspend thaws, a second Unsuspend is refused, and an empty
// session is refused either way (issue #572).
func TestSuspendThawCycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := Suspend(dir, "", "x", time.Time{}); err == nil {
		t.Error("Suspend with no session succeeded, want a refusal")
	}
	if err := Suspend(dir, "lead", "freeze", time.Time{}); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	events, _ := ReadEvents(dir)
	s := FactorySuspended(events, time.Now())
	if !s.Suspended || s.By != "lead" || s.Reason != "freeze" || s.Since == "" || s.Until != "" {
		t.Fatalf("FactorySuspended = %+v, want suspended by lead: freeze", s)
	}
	err := Suspend(dir, "other", "again", time.Time{})
	wantSuspendedRefusal(t, err, "second Suspend")
	if !strings.Contains(err.Error(), "by lead") || !strings.Contains(err.Error(), s.Since) {
		t.Errorf("second Suspend = %v, want it to name since and by", err)
	}
	if _, err := Unsuspend(dir, "", "x"); err == nil {
		t.Error("Unsuspend with no session succeeded, want a refusal")
	}
	if _, err := Unsuspend(dir, "lead", "thaw"); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	events, _ = ReadEvents(dir)
	if s := FactorySuspended(events, time.Now()); s.Suspended {
		t.Errorf("after Unsuspend FactorySuspended = %+v, want thawed", s)
	}
	_, err = Unsuspend(dir, "lead", "again")
	wantSuspendedRefusal(t, err, "Unsuspend when not suspended")
}

// TestSuspendUntilExpiresWithoutEvent: a suspension whose Until has passed at
// the injected now is thawed with no unsuspended event.
func TestSuspendUntilExpiresWithoutEvent(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	events := []Event{{TS: at.Format(time.RFC3339), Kind: "suspended", Session: "lead", Note: "lunch",
		Until: at.Add(time.Hour).Format(time.RFC3339)}}
	if s := FactorySuspended(events, at.Add(30*time.Minute)); !s.Suspended || s.Until == "" {
		t.Errorf("before Until FactorySuspended = %+v, want suspended", s)
	}
	if s := FactorySuspended(events, at.Add(time.Hour)); s.Suspended {
		t.Errorf("at Until FactorySuspended = %+v, want thawed", s)
	}
}

// TestSuspendRefusesRun: Run of a planned task while suspended is refused
// with rule suspended and appends nothing.
func TestSuspendRefusesRun(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatal(err)
	}
	if err := Suspend(dir, "lead", "freeze", time.Time{}); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, ".flywheel", "events.jsonl")
	before, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(dir, RunOptions{Task: "T1"})
	wantSuspendedRefusal(t, err, "Run")
	if after, _ := os.ReadFile(log); string(after) != string(before) {
		t.Errorf("Run while suspended changed the ledger:\n%s", after)
	}
}

// TestSuspendStopsResumeLimited: the rate-limited fixture auto-resumes
// nothing while suspended, reporting the unit not started, reason suspended.
func TestSuspendStopsResumeLimited(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	evs := append(limitedUnit("T", "m", "rate-limited", recoverNow.Add(-time.Hour)),
		Event{Kind: "suspended", Session: "lead", Note: "freeze"})
	recoverLedger(t, dir, evs...)
	var calls []string
	res := resumeLimitedPass(t, dir, true, &calls)
	if len(calls) != 0 {
		t.Errorf("Start calls = %q, want none", calls)
	}
	if len(res.Resumed) != 1 || res.Resumed[0].Started || res.Resumed[0].Reason != "suspended" {
		t.Errorf("Resumed = %+v, want T not started, reason suspended", res.Resumed)
	}
	if ev := autoResumes(t, dir); len(ev) != 0 {
		t.Errorf("auto-resume events = %+v, want none", ev)
	}
}

// TestSuspendNextAndAndon: next offers no DISPATCH while suspended, and the
// floor's andon lists the suspension first.
func TestSuspendNextAndAndon(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("owns: a.go\nneeds: none\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	ts := func(m int) string { return at.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	for _, e := range []Event{
		{TS: ts(0), Task: "L", Kind: "planned", Brief: "b.md", Owns: []string{"l.go"}},
		{TS: ts(1), Task: "L", Kind: "dispatched", Attempt: "r1", Model: "m"},
		{TS: ts(2), Task: "L", Kind: "finished", Attempt: "r1", Model: "m", Reason: "provider-error"},
		{TS: ts(3), Task: "B", Kind: "planned", Brief: "b.md", Owns: []string{"a.go"}},
		{TS: ts(4), Kind: "suspended", Session: "lead", Note: "freeze", Until: at.Add(time.Hour).Format(time.RFC3339)},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	now := at.Add(10 * time.Minute)
	acts, err := NextActions(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	waitB := false
	for _, a := range acts {
		if a.Kind == "DISPATCH" {
			t.Errorf("NextActions offered %+v while suspended", a)
		}
		waitB = waitB || a.Task == "B" && a.Kind == "WAIT" && strings.Contains(a.Reason, "suspended")
	}
	if !waitB {
		t.Errorf("NextActions = %+v, want WAIT B naming the suspension", acts)
	}
	w := NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Andon) < 2 || fl.Andon[0].Task != "factory" ||
		!strings.HasPrefix(fl.Andon[0].State, "factory suspended since ") ||
		!strings.Contains(fl.Andon[0].State, "by lead: freeze until ") {
		t.Errorf("Andon = %+v, want the suspension first, ahead of L", fl.Andon)
	}
}

// TestSuspendStopEndsLiveRun: a worktree Run whose sim worker started a
// session and is held for ten minutes keeps running through a suspension
// without stop (lease ticks pass, no sentinel); a stopping suspension writes
// the sentinel and ends it with reason suspended well before the delay (a
// hang guard, not a window): exit 6, the finished event keeps the session and
// names the suspension, and the owned file it wrote is checkpointed. Unsuspend
// removes the sentinel and returns the stopped attempt (issue #572).
func TestSuspendStopEndsLiveRun(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "long.jsonl")
	lines := `{"type":"step_start","sessionID":"ses_ss","part":{"type":"step_start","step":1}}
{"type":"step_finish","sessionID":"ses_ss","part":{"type":"step_finish","reason":"stop","tokens":{"input":1,"output":1}}}
`
	if err := os.WriteFile(fixture, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := simConfig(fixture)
	cfg.Lease = &LeaseConfig{RenewInterval: "20ms", TTL: "2s"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	var res Result
	var runErr error
	done := make(chan struct{})
	t.Cleanup(func() { <-done }) // a failed test still waits for Run before its temp dirs go
	go func() {
		defer close(done)
		res, runErr = Run(dir, RunOptions{Task: "T1", Worktree: true, SimLineDelay: 10 * time.Minute})
	}()
	waitMidRun(t, done, "the worker's session", func() bool {
		evs, _ := ReadEvents(dir)
		return slices.ContainsFunc(evs, func(e Event) bool { return e.Kind == "started" && e.Session == "ses_ss" })
	})
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // mid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	renewed := func() string {
		ls, _ := ReadLeases(dir)
		if len(ls) != 1 {
			return ""
		}
		return ls[0].RenewedAt
	}
	if err := Suspend(dir, "lead", "freeze", time.Time{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // lease ticks after the suspension: each would have stopped it
		prev := renewed()
		waitMidRun(t, done, "a lease tick", func() bool { r := renewed(); return r != "" && r != prev })
	}
	if _, err := os.Stat(suspendStopPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("sentinel after Suspend without stop: %v, want none", err)
	}
	if _, err := Unsuspend(dir, "lead", "thaw"); err != nil {
		t.Fatal(err)
	}
	if err := SuspendWith(dir, "lead", SuspendOptions{Reason: "freeze", Stop: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(suspendStopPath(dir)); err != nil {
		t.Fatalf("sentinel after Suspend with stop: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("hang guard: Run did not stop on a stopping suspension")
	}
	if runErr != nil || res.Reason != "suspended" || res.Session != "ses_ss" || ExitCode(res) != 6 {
		t.Fatalf("Run() = %+v, %v (exit %d); want reason suspended, session ses_ss, exit 6", res, runErr, ExitCode(res))
	}
	evs, _ := ReadEvents(dir)
	since := FactorySuspended(evs, time.Now()).Since
	var fin Event
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "finished" {
			fin = e
		}
	}
	if fin.Reason != "suspended" || fin.Session != "ses_ss" || !strings.Contains(fin.Note, "stopped by suspend at "+since) || fin.Checkpoint == "" {
		t.Fatalf("finished = %+v, want reason suspended, session ses_ss, the suspension's ts %s and a checkpoint", fin, since)
	}
	if body, err := gitWith(dir, nil, "show", checkpointRef("T1", "r1")+":a.go"); err != nil || body != "package a // mid" {
		t.Errorf("checkpoint a.go = %q, %v", body, err)
	}
	units, err := Unsuspend(dir, "lead", "thaw")
	if err != nil || !slices.Equal(units, []TaskAttempt{{Task: "T1", Attempt: "r1", Session: "ses_ss"}}) {
		t.Errorf("Unsuspend = %+v, %v; want T1 r1 ses_ss", units, err)
	}
	if _, err := os.Stat(suspendStopPath(dir)); !os.IsNotExist(err) {
		t.Errorf("sentinel after Unsuspend: %v, want removed", err)
	}
}
