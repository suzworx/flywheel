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
	t.Parallel()
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

// TestRecoverNeedsOwner checks a unit whose open blocking findings lie outside
// owns reads assign-owner naming them (issue #458), only when not in flight,
// and that --apply lists it and never acts on it.
func TestRecoverNeedsOwner(t *testing.T) {
	t.Parallel()
	ids := []string{"T1-r1-2"}
	for _, f := range []recoverFacts{
		{Task: "T1", Status: "finished", FinishReason: "stop", NeedsOwner: ids},
		{Task: "T1", Status: "needs-correction", NeedsOwner: ids},
	} {
		if n := nextAction(f); n.Action != "assign-owner" || !strings.Contains(n.Reason, "1 open blocking finding(s) outside owns: T1-r1-2;") {
			t.Errorf("nextAction(%s) = %+v, want assign-owner naming T1-r1-2", f.Status, n)
		}
	}
	for _, f := range []recoverFacts{
		{Task: "T1", Status: "running", LeaseLive: true, NeedsOwner: ids},
		{Task: "T1", Status: "dispatched", NeedsOwner: ids},
		{Task: "T1", Status: "landed", NeedsOwner: ids},
	} {
		if n := nextAction(f); n.Action == "assign-owner" {
			t.Errorf("nextAction(%s) = %+v, want no assign-owner", f.Status, n)
		}
	}

	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	blockerB := blockerA
	blockerB.File, blockerB.Claim = "b.go", "Loses the header"
	recoverLedger(t, dir, append([]Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{Task: "T1", Kind: "finished", Attempt: "r1", Reason: "stop"},
	}, roundEvents(1, blockerA, blockerB)...)...)
	out, err := RecoverApply(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatalf("RecoverApply: %v", err)
	}
	n := out.Report.Tasks[0].Next
	if n.Action != "assign-owner" || !strings.Contains(n.Reason, "outside owns: T1-r1-2;") || strings.Contains(n.Reason, "T1-r1-1") {
		t.Errorf("next = %+v, want assign-owner naming only T1-r1-2", n)
	}
	if len(out.Applied) != 0 || !strings.Contains(strings.Join(out.Left, "\n"), "assign-owner T1") {
		t.Errorf("applied = %q, left = %q; want nothing applied and assign-owner T1 left", out.Applied, out.Left)
	}
}

// TestRecoverApplySafeOnly checks --apply runs only the safe actions (issue
// #422): the dead attempt is marked lost and one recovered event names it,
// while land and resume-session are listed, never run.
func TestRecoverApplySafeOnly(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestRecoverGroupsByLead checks integrity failures are grouped by the lead
// that dispatched each unit, the current session first (issue #472), and a
// unit another lead dispatched is marked, while a ledger with no lead recorded
// keeps the flat lines.
func TestRecoverGroupsByLead(t *testing.T) {
	t.Parallel()
	failing := func(task, lead string) []Event {
		return []Event{{Task: task, Kind: "planned", Brief: "gone.txt"},
			{Task: task, Kind: "dispatched", Attempt: "r1", Lead: lead},
			{Task: task, Kind: "finished", Attempt: "r1", Reason: "stop"}}
	}
	dir := t.TempDir()
	recoverLedger(t, dir, slices.Concat(failing("A", "lead-a"), failing("B", "lead-b"), failing("C", ""))...)
	rep, err := Recover(dir, recoverNow, RecoverOptions{Session: "lead-b"})
	if err != nil {
		t.Fatal(err)
	}
	g := rep.Integrity.ByLead
	if len(g) != 3 || g[0].Lead != "lead-b" || !g[0].Current || g[1].Lead != "lead-a" || g[1].Current || g[2].Lead != "" || g[2].Current {
		t.Fatalf("by_lead = %+v", g)
	}
	n := 0
	for i, task := range []string{"B", "A", "C"} {
		if len(g[i].Items) == 0 {
			t.Errorf("group %q has no items", g[i].Lead)
		}
		for _, it := range g[i].Items {
			n++
			if it.Task != task {
				t.Errorf("group %q holds %s's item %+v", g[i].Lead, it.Task, it)
			}
		}
	}
	if n != len(rep.Integrity.Failed) {
		t.Errorf("groups hold %d items, failed has %d", n, len(rep.Integrity.Failed))
	}
	text := rep.Text()
	ib, ia, iu := strings.Index(text, "  lead lead-b (this session): "), strings.Index(text, "  lead lead-a: "), strings.Index(text, "  lead unrecorded: ")
	if ib < 0 || ia < ib || iu < ia {
		t.Errorf("groups out of order (%d, %d, %d):\n%s", ib, ia, iu, text)
	}
	if !strings.Contains(text, "\nA ") || !strings.Contains(text, " [lead lead-a]\n") {
		t.Errorf("A's line with its lead mark is missing:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "A ") && !strings.HasSuffix(line, " [lead lead-a]"):
			t.Errorf("A's line lacks its lead: %q", line)
		case (strings.HasPrefix(line, "B ") || strings.HasPrefix(line, "C ")) && strings.Contains(line, "[lead "):
			t.Errorf("a unit of this session or unrecorded is marked: %q", line)
		}
	}
	flat := t.TempDir()
	recoverLedger(t, flat, slices.Concat(failing("A", ""), failing("C", ""))...)
	if rep, err = Recover(flat, recoverNow, RecoverOptions{Session: "lead-b"}); err != nil {
		t.Fatal(err)
	}
	want := "integrity: FAIL\n"
	for _, it := range rep.Integrity.Failed {
		want += "  " + it.Rule + " " + it.Task + ": " + it.Reason + "\n"
	}
	if text := rep.Text(); !strings.Contains(text, want) || strings.Contains(text, "lead ") {
		t.Errorf("no lead recorded: want the flat lines\n%s\ngot:\n%s", want, text)
	}
}

// TestRecoverDormant checks a unit untouched past DormantAfter is dormant:
// reported, its Next kept, and --apply never re-validates it, while a fresh
// unit with the same action is re-validated.
func TestRecoverDormant(t *testing.T) {
	t.Parallel()
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
