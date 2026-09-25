package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var recoverNow = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

// recoverLedger appends evs (TS filled from a minute counter when empty) to a
// fresh flywheel dir.
func recoverLedger(t *testing.T, dir string, evs ...Event) {
	t.Helper()
	for i, e := range evs {
		if e.TS == "" {
			e.TS = recoverNow.Add(-48*time.Hour + time.Duration(i)*time.Minute).Format(time.RFC3339)
		}
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent(%+v): %v", e, err)
		}
	}
}

// TestRecoverNextActions checks nextAction per status on one synthetic
// ledger each (issue #422).
func TestRecoverNextActions(t *testing.T) {
	rc0 := new(int)
	planned := Event{Task: "T", Kind: "planned", Brief: "brief.txt"}
	disp := Event{Task: "T", Kind: "dispatched", Attempt: "r1", Model: "m"}
	fin := func(reason, resetAt string) Event {
		return Event{Task: "T", Kind: "finished", Attempt: "r1", Model: "m", Reason: reason, ResetAt: resetAt}
	}
	future, past := recoverNow.Add(time.Hour).Format(time.RFC3339), recoverNow.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name  string
		evs   []Event
		lease string // "", "live" or "dead"
		want  string
	}{
		{"planned", []Event{planned}, "", "none"},
		{"dispatched-dead-lease", []Event{planned, disp}, "dead", "mark-lost"},
		{"running-live-lease", []Event{planned, disp, {Task: "T", Kind: "started", Attempt: "r1"}}, "live", "none"},
		{"rate-limited-paused", []Event{planned, disp, fin("rate-limited", future)}, "", "wait-reset"},
		{"rate-limited-reset-passed", []Event{planned, disp, fin("rate-limited", past)}, "", "resume-session"},
		{"abandoned-job", []Event{planned, disp, fin("abandoned-job", "")}, "", "resume-session"},
		{"finished-no-reading", []Event{planned, disp, fin("stop", "")}, "", "re-validate"},
		{"finished-readings-pass", []Event{planned, disp, fin("stop", ""),
			{Task: "T", Kind: "validated", Attempt: "r1", Gate: "g", Tree: "abc", RC: rc0},
			{Task: "T", Kind: "owns_checked", Attempt: "r1", Tree: "abc"}}, "", "inspect"},
		{"passed", []Event{planned, disp, fin("stop", ""), {Task: "T", Kind: "reviewed", Verdict: "pass", Session: "lead"}}, "", "land"},
		{"lost", []Event{planned, disp, {Task: "T", Kind: "lost", Attempt: "r1", Reason: "idle"}}, "", "none"},
		{"rejected", []Event{planned, disp, fin("stop", ""), {Task: "T", Kind: "reviewed", Verdict: "reject", Session: "lead"}}, "", "none"},
		{"landed", []Event{planned, disp, fin("stop", ""), {Task: "T", Kind: "landed", Commit: "abcdef1"}}, "", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			recoverLedger(t, dir, c.evs...)
			if c.lease != "" {
				exp := recoverNow.Add(-time.Hour)
				if c.lease == "live" {
					exp = recoverNow.Add(time.Hour)
				}
				if err := WriteLease(dir, Lease{Task: "T", Attempt: "r1", ExpiresAt: exp.Format(time.RFC3339)}); err != nil {
					t.Fatal(err)
				}
			}
			rep, err := Recover(dir, recoverNow, RecoverOptions{})
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			if len(rep.Tasks) != 1 || rep.Tasks[0].Next.Action != c.want {
				t.Fatalf("next = %+v, want %s", rep.Tasks, c.want)
			}
		})
	}
}

// TestRecoverApplySafeOnly checks --apply runs only the safe actions (issue
// #422): the dead attempt is marked lost and one recovered event names it,
// while land and resume-session are listed, never run.
func TestRecoverApplySafeOnly(t *testing.T) {
	dir := t.TempDir()
	past := recoverNow.Add(-time.Hour).Format(time.RFC3339)
	recoverLedger(t, dir,
		Event{Task: "A", Kind: "planned", Brief: "a.txt"},
		Event{Task: "A", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "B", Kind: "planned", Brief: "b.txt"},
		Event{Task: "B", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "B", Kind: "finished", Attempt: "r1", Reason: "stop"},
		Event{Task: "B", Kind: "reviewed", Verdict: "pass", Session: "lead"},
		Event{Task: "C", Kind: "planned", Brief: "c.txt"},
		Event{Task: "C", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "C", Kind: "finished", Attempt: "r1", Model: "m", Reason: "rate-limited", ResetAt: past})
	if err := WriteLease(dir, Lease{Task: "A", Attempt: "r1", ExpiresAt: past}); err != nil {
		t.Fatal(err)
	}
	out, err := RecoverApply(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatalf("RecoverApply: %v", err)
	}
	if len(out.Applied) != 1 || !strings.HasPrefix(out.Applied[0], "mark-lost A r1") {
		t.Errorf("applied = %q, want only mark-lost A r1", out.Applied)
	}
	left := strings.Join(out.Left, "\n")
	if !strings.Contains(left, "land B") || !strings.Contains(left, "resume-session C") {
		t.Errorf("left = %q, want land B and resume-session C listed", out.Left)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered := 0
	for _, e := range events {
		switch {
		case e.Kind == "recovered":
			recovered++
			if !slices.Equal(e.Paths, []string{"A"}) || !strings.Contains(e.Note, "mark-lost A r1") {
				t.Errorf("recovered = %+v", e)
			}
		case e.Kind == "landed" || e.Kind == "dispatched" && e.Attempt != "r1":
			t.Errorf("apply ran an unsafe action: %+v", e)
		}
	}
	st := Derive(events)
	if recovered != 1 || st.Counts["lost"] != 1 || st.Counts["passed"] != 1 {
		t.Errorf("recovered events = %d, counts = %v", recovered, st.Counts)
	}
}

// TestRecoverReport checks the world checks on a task worktree: HEAD matches
// the attempt's commit, a file no attempt wrote is unexplained (investigate,
// not OK), and a run file cut mid-line is torn.
func TestRecoverReport(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(dir, ".flywheel", "worktrees", "T")
	gitOut(t, dir, "worktree", "add", "-q", wt, "-b", "fw/T")
	head := gitOut(t, wt, "rev-parse", "HEAD")
	for p, body := range map[string]string{"src/a.go": "package a\n", "notes.txt": "stray\n"} {
		full := filepath.Join(wt, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recoverLedger(t, dir, Event{Task: "T", Kind: "planned", Brief: "b.txt"},
		Event{Task: "T", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "T", Kind: "finished", Attempt: "r1", Reason: "error", Commit: head, Wrote: []string{filepath.Join(wt, "src", "a.go")}})
	runs := filepath.Join(dir, ".flywheel", "runs")
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "T.r1.jsonl"), []byte("{}\n{\"cut"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Recover(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	tk := rep.Tasks[0]
	if tk.Head != head || tk.RunFile != "torn" || !slices.Equal(tk.Unexplained, []string{"notes.txt"}) || tk.Next.Action != "investigate" {
		t.Fatalf("task = %+v", tk)
	}
	if rep.OK() || !strings.Contains(rep.Text(), "unexplained: notes.txt") {
		t.Errorf("OK = %v, text:\n%s", rep.OK(), rep.Text())
	}
}

// TestRecoverHeadAncestry checks a lead commit on top of the attempt's commit
// is consistent, and a HEAD that does not contain it is investigate.
func TestRecoverHeadAncestry(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(dir, ".flywheel", "worktrees", "T")
	gitOut(t, dir, "worktree", "add", "-q", wt, "-b", "fw/T")
	commit := func(d, msg string) string {
		gitOut(t, d, "-c", "user.name=t", "-c", "user.email=t@e.x", "commit", "-q", "--allow-empty", "-m", msg)
		return gitOut(t, d, "rev-parse", "HEAD")
	}
	attempt := commit(wt, "attempt")
	commit(wt, "lead on top")
	recoverLedger(t, dir, Event{Task: "T", Kind: "planned", Brief: "b.txt"},
		Event{Task: "T", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "T", Kind: "finished", Attempt: "r1", Reason: "stop", Commit: attempt})
	rep, err := Recover(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if a := rep.Tasks[0].Next.Action; a == "investigate" {
		t.Fatalf("descendant HEAD: next = %+v, want no investigate", rep.Tasks[0].Next)
	}
	gitOut(t, wt, "reset", "-q", "--hard", commit(dir, "unrelated on main"))
	if rep, err = Recover(dir, recoverNow, RecoverOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := rep.Tasks[0].Next; n.Action != "investigate" || !strings.Contains(n.Reason, "does not contain") {
		t.Errorf("unrelated HEAD: next = %+v, want investigate", n)
	}
}

// TestRecoverHistorySplit checks a rule failure on a landed unit is history
// (integrity still passes, Text summarises landed units) while one on a unit
// not landed fails integrity.
func TestRecoverHistorySplit(t *testing.T) {
	dir := t.TempDir()
	recoverLedger(t, dir, Event{Task: "L", Kind: "planned", Brief: "gone.txt"},
		Event{Task: "L", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "L", Kind: "finished", Attempt: "r1", Reason: "stop"},
		Event{Task: "L", Kind: "landed", Commit: "abcdef1"})
	rep, err := Recover(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := rep.Text()
	if !rep.Integrity.Pass || len(rep.Integrity.Failed) != 0 || len(rep.Integrity.History) == 0 ||
		!strings.Contains(text, "history: ") || !strings.Contains(text, "1 landed units") || strings.Contains(text, "\nL landed") {
		t.Fatalf("integrity = %+v\n%s", rep.Integrity, text)
	}
	if all, _ := Recover(dir, recoverNow, RecoverOptions{All: true}); !strings.Contains(all.Text(), "\nL landed") {
		t.Errorf("--all text lacks the landed unit:\n%s", all.Text())
	}
	recoverLedger(t, dir, Event{Task: "N", Kind: "planned", Brief: "gone.txt"},
		Event{Task: "N", Kind: "dispatched", Attempt: "r1"},
		Event{Task: "N", Kind: "finished", Attempt: "r1", Reason: "stop"})
	if rep, err = Recover(dir, recoverNow, RecoverOptions{}); err != nil {
		t.Fatal(err)
	}
	if rep.Integrity.Pass || len(rep.Integrity.Failed) == 0 || rep.Integrity.Failed[0].Task != "N" {
		t.Errorf("a failure on N must fail integrity: %+v", rep.Integrity)
	}
}

// TestRecoverDormant checks a unit untouched past DormantAfter is dormant:
// reported, its Next kept, and --apply never re-validates it, while a fresh
// unit with the same action is re-validated.
func TestRecoverDormant(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"}) // T1 planned, brief.txt owns a.go
	if err != nil {
		t.Fatal(err)
	}
	old := recoverNow.Add(-30 * 24 * time.Hour)
	at := func(t0 time.Time, m int) string { return t0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	recoverLedger(t, dir,
		Event{TS: at(recoverNow, -60), Task: "T1", Kind: "dispatched", Attempt: "r1"},
		Event{TS: at(recoverNow, -59), Task: "T1", Kind: "finished", Attempt: "r1", Reason: "stop"},
		Event{TS: at(old, 0), Task: "Old", Kind: "planned", Brief: "brief.txt"},
		Event{TS: at(old, 1), Task: "Old", Kind: "dispatched", Attempt: "r1"},
		Event{TS: at(old, 2), Task: "Old", Kind: "finished", Attempt: "r1", Reason: "stop"})
	o := RecoverOptions{DormantAfter: 168 * time.Hour}
	rep, err := Recover(dir, recoverNow, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range rep.Tasks {
		if tk.Dormant != (tk.Task == "Old") || tk.Next.Action != "re-validate" {
			t.Errorf("%s: dormant=%v next=%s", tk.Task, tk.Dormant, tk.Next.Action)
		}
	}
	if !strings.Contains(rep.Text(), "1 dormant units") {
		t.Errorf("text lacks the dormant summary:\n%s", rep.Text())
	}
	out, err := RecoverApply(dir, recoverNow, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out.Applied, "re-validate T1") || !strings.Contains(strings.Join(out.Left, "\n"), "dormant Old") {
		t.Errorf("applied = %q, left = %q", out.Applied, out.Left)
	}
	events, _ := ReadEvents(dir)
	for _, e := range events {
		if e.Task == "Old" && e.Kind == "validated" {
			t.Errorf("apply validated the dormant unit: %+v", e)
		}
	}
}
