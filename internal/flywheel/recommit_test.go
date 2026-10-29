package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recommitGit runs git in dir with a fixed identity and no line-ending
// conversion, returning its trimmed output and failing the test on error.
func recommitGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// recommitRepo makes a repo whose task T1 (owns a.go) has a task worktree at
// .flywheel/worktrees/T1 on fw/T1, and an event log with planned, dispatched
// and a finished r1 that left uncommitted. It returns the repo and worktree.
func recommitRepo(t *testing.T, uncommitted ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	recommitGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recommitGit(t, dir, "add", "-A")
	recommitGit(t, dir, "commit", "-q", "-m", "initial")
	wt := filepath.Join(dir, ".flywheel", "worktrees", "T1")
	recommitGit(t, dir, "worktree", "add", "-q", "-b", "fw/T1", wt)
	h := &BriefHeader{Owns: []string{"a.go"}}
	for _, e := range []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt", Owns: h.Owns, Header: h},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt", Header: h},
		{Task: "T1", Kind: "finished", Attempt: "r1", Uncommitted: uncommitted},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	return dir, wt
}

// recommitAmend records an amended event widening T1's owns to owns.
func recommitAmend(t *testing.T, dir string, owns ...string) {
	t.Helper()
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "amended", Brief: "brief.txt", Owns: owns, Header: &BriefHeader{Owns: owns}}); err != nil {
		t.Fatal(err)
	}
}

// writeFiles writes each name in wt with a small body.
func writeFiles(t *testing.T, wt string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(wt, n), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// recommitLast returns the log's last event.
func recommitLast(t *testing.T, dir string) Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("ReadEvents: %d events, err %v", len(events), err)
	}
	return events[len(events)-1]
}

// TestRecommitCommitsAmendedOwns checks issue #897: a path the attempt commit
// left out is named by the owns check; once owns is amended to cover it,
// WorktreeCommit commits it on fw/T1, records recommitted, and the check
// names nothing. The task's status stays finished.
func TestRecommitCommitsAmendedOwns(t *testing.T) {
	t.Parallel()
	dir, wt := recommitRepo(t, "b.go")
	writeFiles(t, wt, "b.go")
	events, _ := ReadEvents(dir)
	if left := leftUncommitted(wt, events, "T1"); !slices.Equal(left, []string{"b.go"}) {
		t.Fatalf("before: leftUncommitted = %v, want [b.go]", left)
	}
	recommitAmend(t, dir, "a.go", "b.go")
	res, err := WorktreeCommit(dir, "T1", WorktreeCommitOptions{Session: "lead", Note: "owns amended"})
	if err != nil {
		t.Fatalf("WorktreeCommit: %v", err)
	}
	if res.Commit == "" || res.Attempt != "r1" || len(res.Uncommitted) != 0 {
		t.Fatalf("result = %+v, want a commit for r1 and nothing left", res)
	}
	if got := recommitGit(t, dir, "rev-parse", "fw/T1"); got != res.Commit {
		t.Errorf("fw/T1 = %s, want %s", got, res.Commit)
	}
	if got := recommitGit(t, dir, "log", "--format=%s", "-1", "fw/T1"); got != "T1 r1" {
		t.Errorf("fw/T1 subject = %q, want %q", got, "T1 r1")
	}
	if st := recommitGit(t, wt, "status", "--porcelain", "--", "b.go"); st != "" {
		t.Errorf("b.go not clean after the commit: %q", st)
	}
	ev := recommitLast(t, dir)
	if ev.Kind != "recommitted" || ev.Commit != res.Commit || ev.Session != "lead" || ev.Attempt != "r1" || len(ev.Uncommitted) != 0 || ev.Note != "owns amended" {
		t.Errorf("event = %+v, want recommitted with the commit and no uncommitted", ev)
	}
	events, _ = ReadEvents(dir)
	if left := leftUncommitted(wt, events, "T1"); len(left) != 0 {
		t.Errorf("after: leftUncommitted = %v, want none", left)
	}
	for _, ts := range Derive(events).Tasks {
		if ts.ID == "T1" && ts.Status != "finished" {
			t.Errorf("status = %s after recommitted, want finished", ts.Status)
		}
	}
}

// TestRecommitKeepsPathOutsideOwns checks that a changed path the amended owns
// still does not cover is recorded on recommitted and still named by the
// owns check, even when the finished event never listed it.
func TestRecommitKeepsPathOutsideOwns(t *testing.T) {
	t.Parallel()
	dir, wt := recommitRepo(t, "b.go")
	writeFiles(t, wt, "b.go", "c.go")
	recommitAmend(t, dir, "a.go", "b.go")
	res, err := WorktreeCommit(dir, "T1", WorktreeCommitOptions{Session: "lead"})
	if err != nil {
		t.Fatalf("WorktreeCommit: %v", err)
	}
	if res.Commit == "" || !slices.Equal(res.Uncommitted, []string{"c.go"}) {
		t.Fatalf("result = %+v, want a commit and [c.go] left", res)
	}
	if ev := recommitLast(t, dir); ev.Kind != "recommitted" || !slices.Equal(ev.Uncommitted, []string{"c.go"}) {
		t.Errorf("event = %+v, want recommitted naming c.go", ev)
	}
	events, _ := ReadEvents(dir)
	if left := leftUncommitted(wt, events, "T1"); !slices.Equal(left, []string{"c.go"}) {
		t.Errorf("leftUncommitted = %v, want [c.go]", left)
	}
}

// TestRecommitRefusesWithoutTaskWorktree checks that an unknown task, and a
// known task with no task worktree, are errors that append nothing.
func TestRecommitRefusesWithoutTaskWorktree(t *testing.T) {
	t.Parallel()
	dir, wt := recommitRepo(t, "b.go")
	recommitGit(t, dir, "worktree", "remove", "--force", wt)
	before, _ := ReadEvents(dir)
	for _, task := range []string{"T9", "T1"} {
		if _, err := WorktreeCommit(dir, task, WorktreeCommitOptions{Session: "lead"}); err == nil {
			t.Errorf("%s: WorktreeCommit succeeded, want an error", task)
		}
	}
	if after, _ := ReadEvents(dir); len(after) != len(before) {
		t.Errorf("events %d -> %d, want none appended", len(before), len(after))
	}
}

// TestRecommitEventValidation checks the events rules: uncommitted only on
// finished and recommitted, and recommitted needs a task and a session.
func TestRecommitEventValidation(t *testing.T) {
	t.Parallel()
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Uncommitted: []string{"x.go"}}); err == nil {
		t.Error("uncommitted on dispatched accepted")
	}
	if err := Validate(Event{Task: "T1", Kind: "recommitted", Attempt: "r1", Uncommitted: []string{"x.go"}}); err == nil {
		t.Error("recommitted without a session accepted")
	}
	if err := Validate(Event{Kind: "recommitted", Session: "lead"}); err == nil {
		t.Error("recommitted without a task accepted")
	}
	if err := Validate(Event{Task: "T1", Kind: "recommitted", Attempt: "r1", Session: "lead", Uncommitted: []string{"x.go"}}); err != nil {
		t.Errorf("valid recommitted refused: %v", err)
	}
}
