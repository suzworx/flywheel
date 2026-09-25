package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// stackedRepo builds the #414 shape: main; fw/A with two commits; fw/B
// branched from fw/A with one commit of its own, checked out in B's task
// worktree; then A squash-merged onto main with its trailer. B's dispatch
// records fw/A's tip as its base. It returns dir, B's base and the squash.
func stackedRepo(t *testing.T) (dir, base, squash string) {
	t.Helper()
	dir = t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	write := func(d, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(dir, ".gitignore", ".flywheel/\nflywheel.md\n")
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "ignore"})
	git(t, dir, []string{"branch", "-M", "main"})
	git(t, dir, []string{"checkout", "-q", "-b", "fw/A"})
	for _, f := range []string{"a1.go", "a2.go"} {
		write(dir, f, "package x // "+f+"\n")
		git(t, dir, []string{"add", f})
		git(t, dir, []string{"commit", "-m", "A " + f, "-m", "Flywheel-Task: A"})
	}
	base = git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"checkout", "-q", "main"})
	wt := filepath.Join(dir, ".flywheel", "worktrees", "B")
	git(t, dir, []string{"worktree", "add", "-q", "-b", "fw/B", wt, "fw/A"})
	write(wt, "b.go", "package x // B\n")
	git(t, wt, []string{"add", "b.go"})
	git(t, wt, []string{"commit", "-m", "B r1", "-m", "Flywheel-Task: B"})
	git(t, dir, []string{"merge", "-q", "--squash", "fw/A"})
	git(t, dir, []string{"commit", "-m", "A (#1)", "-m", "Flywheel-Task: A"})
	squash = git(t, dir, []string{"rev-parse", "HEAD"})
	if err := AppendEvent(dir, Event{Task: "B", Kind: "dispatched", Attempt: "r1", Base: base, Workdir: wt}); err != nil {
		t.Fatal(err)
	}
	return dir, base, squash
}

// TestSquashedBase checks SquashedBase (#414): a unit based on a unit that
// landed as a squash is stacked, naming the squash and the base unit; a unit
// based on main is not.
func TestSquashedBase(t *testing.T) {
	t.Parallel()
	dir, base, squash := stackedRepo(t)
	first := git(t, dir, []string{"rev-list", "--max-parents=0", "main"})
	if err := AppendEvent(dir, Event{Task: "C", Kind: "dispatched", Attempt: "r1", Base: first}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotBase, landedAs, baseTask, ok := SquashedBase(dir, events, "B")
	if !ok || gotBase != base || landedAs != squash || baseTask != "A" {
		t.Fatalf("SquashedBase(B) = %q, %q, %q, %v; want %q, %q, A, true", gotBase, landedAs, baseTask, ok, base, squash)
	}
	if _, _, _, ok := SquashedBase(dir, events, "C"); ok {
		t.Fatalf("SquashedBase(C) ok for a unit based on main")
	}
	if _, _, _, ok := SquashedBase(dir, events, "nobody"); ok {
		t.Fatalf("SquashedBase ok for a unit with no recorded base")
	}
}

// TestRebaseUnit checks RebaseUnit (#414): the stacked unit rebases onto main
// cleanly and records a rebased event that dispatchBase then honours; a
// conflicting rebase is aborted, names the paths and leaves the branch as is.
func TestRebaseUnit(t *testing.T) {
	t.Parallel()
	dir, base, squash := stackedRepo(t)
	newBase, conflicts, err := RebaseUnit(dir, "B", "")
	if err != nil || len(conflicts) != 0 || newBase != squash {
		t.Fatalf("RebaseUnit() = %q, %v, %v; want %q, none, nil", newBase, conflicts, err, squash)
	}
	if parent := git(t, dir, []string{"rev-parse", "fw/B~1"}); parent != squash {
		t.Fatalf("fw/B~1 = %s, want the squash %s", parent, squash)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatchBase(events, "B", ""); got != squash {
		t.Fatalf("dispatchBase after rebase = %q, want %q", got, squash)
	}
	last := events[len(events)-1]
	if last.Kind != "rebased" || last.Note != "was "+base+", onto main" {
		t.Fatalf("last event = %+v, want rebased with note was <old>, onto main", last)
	}
	if _, _, _, ok := SquashedBase(dir, events, "B"); ok {
		t.Fatalf("SquashedBase(B) still ok after the rebase")
	}

	dir, _, _ = stackedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x // main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "b.go"})
	git(t, dir, []string{"commit", "-m", "main b"})
	before := git(t, dir, []string{"rev-parse", "fw/B"})
	_, conflicts, err = RebaseUnit(dir, "B", "main")
	if err != nil || !slices.Contains(conflicts, "b.go") {
		t.Fatalf("RebaseUnit() conflicts = %v, err = %v; want b.go, nil", conflicts, err)
	}
	if after := git(t, dir, []string{"rev-parse", "fw/B"}); after != before {
		t.Fatalf("fw/B moved on a conflict: %s -> %s", before, after)
	}
	if events, _ := ReadEvents(dir); events[len(events)-1].Kind == "rebased" {
		t.Fatalf("a conflicting rebase recorded a rebased event")
	}
	if _, _, err := RebaseUnit(dir, "A", ""); err == nil {
		t.Fatalf("RebaseUnit accepted a unit with no task worktree")
	}
}
