package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
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

// squashAnswer is one squashedBase result.
type squashAnswer struct {
	base, landedAs, baseTask string
	ok                       bool
}

// stackedUnits is stackedRepo plus S0..S4 dispatched on B's squashed base and
// M0..M4 on main's root (an ancestor of main). It returns the events, every
// task (B and "nobody", which has no base, included) and SquashedBase's
// fresh answer for each.
func stackedUnits(t *testing.T) (dir string, events []Event, tasks []string, want map[string]squashAnswer) {
	t.Helper()
	dir, base, _ := stackedRepo(t)
	first := git(t, dir, []string{"rev-list", "--max-parents=0", "main"})
	var evs []Event
	tasks = []string{"B", "nobody"}
	for i := range 5 {
		s, m := fmt.Sprintf("S%d", i), fmt.Sprintf("M%d", i)
		evs = append(evs, Event{Task: s, Kind: "dispatched", Attempt: "r1", Base: base},
			Event{Task: m, Kind: "dispatched", Attempt: "r1", Base: first})
		tasks = append(tasks, s, m)
	}
	if err := AppendEvents(dir, evs); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]squashAnswer{}
	for _, task := range tasks {
		b, l, bt, ok := SquashedBase(dir, events, task)
		want[task] = squashAnswer{b, l, bt, ok}
		if stacked := task == "B" || task[0] == 'S'; ok != stacked {
			t.Fatalf("SquashedBase(%s) ok = %v, want %v", task, ok, stacked)
		}
	}
	return dir, events, tasks, want
}

// TestSquashedBaseSharedCache checks one squashCache across many units
// (issue #628): every answer matches SquashedBase's, and the shared cache
// starts at most 7 git processes (mainBranch, the squashed base's ancestry,
// merge-base and log, fw/A's rev-parse and ancestry, the root's ancestry)
// where a fresh cache per unit starts one set per unit.
func TestSquashedBaseSharedCache(t *testing.T) {
	t.Parallel()
	dir, events, tasks, want := stackedUnits(t)
	shared := newSquashCache()
	var fresh int32
	for _, task := range tasks {
		c := newSquashCache()
		squashedBase(dir, events, task, c)
		fresh += c.calls.Load()
		b, l, bt, ok := squashedBase(dir, events, task, shared)
		if got := (squashAnswer{b, l, bt, ok}); got != want[task] {
			t.Errorf("squashedBase(%s, shared) = %+v, want %+v", task, got, want[task])
		}
	}
	const bound = 7
	if got := shared.calls.Load(); got > bound {
		t.Errorf("shared cache git calls = %d, want <= %d", got, bound)
	}
	if fresh <= 5*bound {
		t.Errorf("per-unit git calls = %d, want > %d (one set per unit)", fresh, 5*bound)
	}
	t.Logf("git calls: shared %d, per unit %d", shared.calls.Load(), fresh)
}

// TestSquashedBaseCacheConcurrent checks 8 goroutines sharing one
// squashCache give SquashedBase's answers (run under -race).
func TestSquashedBaseCacheConcurrent(t *testing.T) {
	t.Parallel()
	dir, events, tasks, want := stackedUnits(t)
	shared := newSquashCache()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range tasks {
				task := tasks[(i+g)%len(tasks)]
				b, l, bt, ok := squashedBase(dir, events, task, shared)
				if got := (squashAnswer{b, l, bt, ok}); got != want[task] {
					t.Errorf("goroutine %d: squashedBase(%s) = %+v, want %+v", g, task, got, want[task])
				}
			}
		})
	}
	wg.Wait()
	if got := shared.calls.Load(); got > 7 {
		t.Errorf("shared cache git calls = %d, want <= 7", got)
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
