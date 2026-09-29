package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// driftRepo builds the #672 shape: main at I0; unit T's branch fw/T with one
// commit, forked at I0 in T's task worktree and dispatched on I0; then I1 on
// main, and, with manual, fw/T rebased onto I1 with plain git (outside
// flywheel). It returns dir, the worktree, I0 and I1.
func driftRepo(t *testing.T, manual bool) (dir, wt, i0, i1 string) {
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
	write(dir, ".gitignore", ".flywheel/\nflywheel.md\nsetup.marker\n")
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "I0"})
	git(t, dir, []string{"branch", "-M", "main"})
	i0 = git(t, dir, []string{"rev-parse", "HEAD"})
	wt = filepath.Join(dir, ".flywheel", "worktrees", "T")
	git(t, dir, []string{"worktree", "add", "-q", "-b", "fw/T", wt, "main"})
	write(wt, "t.go", "package x // T\n")
	git(t, wt, []string{"add", "t.go"})
	git(t, wt, []string{"commit", "-m", "T r1", "-m", "Flywheel-Task: T"})
	if err := AppendEvent(dir, Event{Task: "T", Kind: "dispatched", Attempt: "r1", Base: i0, Workdir: wt}); err != nil {
		t.Fatal(err)
	}
	write(dir, "i.txt", "one\n")
	git(t, dir, []string{"add", "i.txt"})
	git(t, dir, []string{"commit", "-m", "I1"})
	i1 = git(t, dir, []string{"rev-parse", "HEAD"})
	if manual {
		git(t, wt, []string{"rebase", "-q", "main"})
	}
	return dir, wt, i0, i1
}

// TestEffectiveBase checks effectiveBase (issue #672): the recorded base when
// the branch forks there, the real fork point after a plain git rebase, and
// the recorded base again when it is not an ancestor of the fork point.
func TestEffectiveBase(t *testing.T) {
	t.Parallel()
	_, wt, i0, _ := driftRepo(t, false)
	if got, drifted := effectiveBase(wt, i0, "main"); got != i0 || drifted {
		t.Fatalf("effectiveBase(clean) = %q, %v; want %q, false", got, drifted, i0)
	}
	dir, wt, i0, i1 := driftRepo(t, true)
	if got, drifted := effectiveBase(wt, i0, "main"); got != i1 || !drifted {
		t.Fatalf("effectiveBase(rebased) = %q, %v; want %q, true", got, drifted, i1)
	}
	git(t, dir, []string{"checkout", "-q", "-b", "side", i0})
	if err := os.WriteFile(filepath.Join(dir, "side.txt"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "side.txt"})
	git(t, dir, []string{"commit", "-m", "side"})
	side := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"checkout", "-q", "main"})
	if got, drifted := effectiveBase(wt, side, "main"); got != side || drifted {
		t.Fatalf("effectiveBase(not an ancestor) = %q, %v; want %q, false", got, drifted, side)
	}
	if got, drifted := effectiveBase(wt, i0, "no-such-ref"); got != i0 || drifted {
		t.Fatalf("effectiveBase(bad ref) = %q, %v; want %q, false", got, drifted, i0)
	}
}

// advanceMain commits I2 on main, changing the file I1 added, so replaying
// I1 from the stale base would conflict. It returns I2.
func advanceMain(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "i.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "i.txt"})
	git(t, dir, []string{"commit", "-m", "I2"})
	return git(t, dir, []string{"rev-parse", "HEAD"})
}

// TestRebaseUnitAfterManualRebase checks RebaseUnit starts from the branch's
// real fork point after a rebase done outside flywheel (issue #672): no
// conflict, only the unit's commit on top of I2, and the Note says so.
func TestRebaseUnitAfterManualRebase(t *testing.T) {
	t.Parallel()
	dir, _, _, i1 := driftRepo(t, true)
	i2 := advanceMain(t, dir)
	newBase, conflicts, err := RebaseUnit(dir, "T", "main")
	if err != nil || len(conflicts) != 0 || newBase != i2 {
		t.Fatalf("RebaseUnit() = %q, %v, %v; want %q, none, nil", newBase, conflicts, err, i2)
	}
	if n := git(t, dir, []string{"rev-list", "--count", i2 + "..fw/T"}); n != "1" {
		t.Fatalf("rev-list I2..fw/T count = %s, want 1", n)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != "rebased" || !strings.Contains(last.Note, "rebased outside flywheel") || !strings.Contains(last.Note, i1) {
		t.Fatalf("last event = %+v, want rebased naming the fork %s and rebased outside flywheel", last, i1)
	}
}

// setSetup sets worktree.setup to command in dir's config.
func setSetup(t *testing.T, dir, command string) {
	t.Helper()
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Worktree = &WorktreeConfig{Setup: command}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
}

// kindCount counts task T's events of kind in dir's log.
func kindCount(t *testing.T, dir, kind string) (n int, last Event) {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Task == "T" && e.Kind == kind {
			n, last = n+1, e
		}
	}
	return n, last
}

// TestRebaseUnitRerunsSetup checks RebaseUnit re-runs worktree.setup in the
// unit's worktree after a rebase (issue #672), recording one worktree_setup
// event; a failing setup is an error naming the command while the rebased
// event is still appended.
func TestRebaseUnitRerunsSetup(t *testing.T) {
	t.Parallel()
	dir, wt, _, _ := driftRepo(t, false)
	setSetup(t, dir, `echo "setting up $FLYWHEEL_TASK" && echo ok > setup.marker`)
	if _, conflicts, err := RebaseUnit(dir, "T", "main"); err != nil || len(conflicts) != 0 {
		t.Fatalf("RebaseUnit() conflicts = %v, err = %v; want none, nil", conflicts, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "setup.marker")); err != nil {
		t.Fatalf("setup.marker not written in the worktree: %v", err)
	}
	n, ev := kindCount(t, dir, "worktree_setup")
	if n != 1 || ev.RC == nil || *ev.RC != 0 || ev.Attempt != "" || !strings.Contains(ev.Note, "setting up T") {
		t.Fatalf("worktree_setup events = %d, last %+v; want one, rc 0, no attempt, the output tail", n, ev)
	}

	dir, _, _, _ = driftRepo(t, false)
	setSetup(t, dir, `echo broken && exit 3`)
	_, _, err := RebaseUnit(dir, "T", "main")
	if err == nil || !strings.Contains(err.Error(), "echo broken && exit 3") || !strings.Contains(err.Error(), "rc=3") {
		t.Fatalf("RebaseUnit() err = %v, want one naming the setup command and rc=3", err)
	}
	if n, _ := kindCount(t, dir, "rebased"); n != 1 {
		t.Fatalf("rebased events = %d, want 1 after a failed setup", n)
	}
	if n, ev := kindCount(t, dir, "worktree_setup"); n != 1 || ev.RC == nil || *ev.RC != 3 {
		t.Fatalf("worktree_setup events = %d, last %+v; want one with rc 3", n, ev)
	}
}

// TestBaseDrift checks the validate reading (issue #672): none on a clean
// unit, and after a plain git rebase one naming both bases and the fix.
func TestBaseDrift(t *testing.T) {
	t.Parallel()
	_, wt, i0, _ := driftRepo(t, false)
	if got := baseDrift(wt, "T", i0, integrationRef(wt)); got != "" {
		t.Fatalf("baseDrift(clean) = %q, want none", got)
	}
	// finishValidate calls baseDrift only when the walk's fork is not the base.
	if _, fork, err := unitChangedPathsRef(wt, i0, "T", integrationRef(wt)); err != nil || fork != i0 {
		t.Fatalf("unitChangedPathsRef(clean) fork = %q, %v; want the base %q", fork, err, i0)
	}
	_, wt, i0, i1 := driftRepo(t, true)
	if _, fork, err := unitChangedPathsRef(wt, i0, "T", integrationRef(wt)); err != nil || fork != i1 {
		t.Fatalf("unitChangedPathsRef(rebased) fork = %q, %v; want the fork %q", fork, err, i1)
	}
	want := "recorded " + short7(i0) + ", branch forks from " + short7(i1) + "; record it: flywheel log --task T --kind rebased --base " + i1
	if got := baseDrift(wt, "T", i0, ""); got != "" {
		t.Fatalf("baseDrift(no integration ref) = %q, want none", got)
	}
	if got := baseDrift(wt, "T", i0, integrationRef(wt)); got != want {
		t.Fatalf("baseDrift(rebased) = %q, want %q", got, want)
	}
}
