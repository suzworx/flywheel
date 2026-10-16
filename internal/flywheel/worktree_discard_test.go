package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// discardRepo makes a repo with one commit and a linked worktree on branch
// wtb, and returns the repo and the worktree paths.
func discardRepo(t *testing.T) (string, string) {
	t.Helper()
	r := t.TempDir()
	initGitRepoAt(t, r)
	writeTestFile(t, r, "a.txt", "a\n")
	git(t, r, []string{"add", "-A"})
	git(t, r, []string{"commit", "-q", "-m", "initial"})
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, r, []string{"worktree", "add", "-q", "-b", "wtb", wt})
	return r, wt
}

// discardEvents returns the worktree_discarded events in dir's ledger.
func discardEvents(t *testing.T, dir string) []Event {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events.jsonl")); os.IsNotExist(err) {
		return nil
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == "worktree_discarded" {
			out = append(out, e)
		}
	}
	return out
}

func TestWorktreeRemoveClean(t *testing.T) {
	t.Parallel()
	r, wt := discardRepo(t)
	res, err := WorktreeRemove(r, wt, WorktreeRemoveOptions{})
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if len(res.Discarded) != 0 {
		t.Fatalf("discarded = %v, want none", res.Discarded)
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	if evs := discardEvents(t, r); len(evs) != 0 {
		t.Fatalf("events = %v, want none", evs)
	}
}

func TestWorktreeRemoveDirtyRefused(t *testing.T) {
	t.Parallel()
	r, wt := discardRepo(t)
	writeTestFile(t, wt, "a.txt", "edited\n")
	writeTestFile(t, wt, "new/u.txt", "untracked\n")
	_, err := WorktreeRemove(r, wt, WorktreeRemoveOptions{})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "worktree-dirty" {
		t.Fatalf("err = %v, want RuleRefusal worktree-dirty", err)
	}
	for _, p := range []string{"a.txt", "new/u.txt"} {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("refusal %q does not name %s", err, p)
		}
	}
	assertTestFile(t, wt, "a.txt", "edited\n")
	assertTestFile(t, wt, "new/u.txt", "untracked\n")
	if evs := discardEvents(t, r); len(evs) != 0 {
		t.Fatalf("events = %v, want none", evs)
	}
}

func TestWorktreeDiscardRecords(t *testing.T) {
	t.Parallel()
	r, wt := discardRepo(t)
	writeTestFile(t, wt, "a.txt", "edited\n")
	writeTestFile(t, wt, "new/u.txt", "untracked\n")
	if _, err := WorktreeRemove(r, wt, WorktreeRemoveOptions{Discard: true}); err == nil {
		t.Fatal("--discard without a session succeeded")
	}
	res, err := WorktreeRemove(r, wt, WorktreeRemoveOptions{Discard: true, Session: "lead-1", Note: "scratch"})
	if err != nil {
		t.Fatalf("WorktreeRemove --discard: %v", err)
	}
	want := []string{"a.txt", "new/u.txt"}
	if !reflect.DeepEqual(res.Discarded, want) {
		t.Fatalf("discarded = %v, want %v", res.Discarded, want)
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	evs := discardEvents(t, r)
	if len(evs) != 1 {
		t.Fatalf("events = %v, want one", evs)
	}
	e := evs[0]
	if !reflect.DeepEqual(e.Paths, want) || e.Session != "lead-1" || !filepath.IsAbs(e.Workdir) || e.Commit == "" {
		t.Fatalf("event = %+v", e)
	}
	if !strings.Contains(e.Note, "wtb") || !strings.Contains(e.Note, "scratch") {
		t.Fatalf("note = %q, want the branch and the note", e.Note)
	}
}

func TestWorktreeRemoveMainRefused(t *testing.T) {
	t.Parallel()
	r, _ := discardRepo(t)
	_, err := WorktreeRemove(r, r, WorktreeRemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), r) {
		t.Fatalf("err = %v, want a refusal naming %s", err, r)
	}
	assertTestFile(t, r, "a.txt", "a\n")
}

func TestWorktreeRemoveNotAWorktree(t *testing.T) {
	t.Parallel()
	r, wt := discardRepo(t)
	other := t.TempDir()
	_, err := WorktreeRemove(r, other, WorktreeRemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("err = %v, want a refusal naming %s", err, other)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree removed: %v", err)
	}
}

func TestWorktreeDiscardEventValidate(t *testing.T) {
	t.Parallel()
	ok := Event{Kind: "worktree_discarded", Workdir: "/w", Paths: []string{"a"}, Session: "s"}
	if err := Validate(ok); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for name, e := range map[string]Event{
		"no workdir": {Kind: "worktree_discarded", Paths: []string{"a"}, Session: "s"},
		"no paths":   {Kind: "worktree_discarded", Workdir: "/w", Session: "s"},
		"no session": {Kind: "worktree_discarded", Workdir: "/w", Paths: []string{"a"}},
	} {
		if err := Validate(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
