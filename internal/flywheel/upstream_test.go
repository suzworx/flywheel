package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// upstreamWrite writes body to name in dir and commits it on the current
// branch, returning the new HEAD.
func upstreamWrite(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", name})
	git(t, dir, []string{"commit", "-q", "-m", msg})
	return git(t, dir, []string{"rev-parse", "HEAD"})
}

// upstreamRepo builds the #770 shape: base B on main with a.go and b.txt,
// fw/T forked at B, then M1 on main adding a blank line and "var X = 9" to
// a.go. fw/T is checked out, M1 not merged. It returns dir, B and M1.
func upstreamRepo(t *testing.T) (dir, base, m1 string) {
	t.Helper()
	dir = t.TempDir()
	initGitRepoAt(t, dir)
	upstreamWrite(t, dir, "b.txt", "b\n", "b")
	base = upstreamWrite(t, dir, "a.go", "package x\n", "B")
	git(t, dir, []string{"branch", "-M", "main"})
	git(t, dir, []string{"checkout", "-q", "-b", "fw/T"})
	git(t, dir, []string{"checkout", "-q", "main"})
	m1 = upstreamWrite(t, dir, "a.go", "package x\n\nvar X = 9\n", "M1")
	git(t, dir, []string{"checkout", "-q", "fw/T"})
	return dir, base, m1
}

// TestUpstreamDropped: a line M1 added that the tree no longer holds is
// listed; kept (with CRLF and trailing space) it is not, and the blank line
// M1 added never counts (issue #770).
func TestUpstreamDropped(t *testing.T) {
	t.Parallel()
	dir, base, m1 := upstreamRepo(t)
	git(t, dir, []string{"merge", "-q", "--ff-only", "main"})
	upstreamWrite(t, dir, "a.go", "package x\nvar u = 1\n", "unit drops X")
	got, err := droppedUpstream(dir, base, m1, []string{"a.go"})
	if want := []string{"a.go: var X = 9"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("droppedUpstream(dropped) = %q, %v; want %q", got, err, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\r\nvar u = 1\r\nvar X = 9  \r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := droppedUpstream(dir, base, m1, []string{"a.go"}); err != nil || got != nil {
		t.Fatalf("droppedUpstream(kept) = %q, %v; want nil", got, err)
	}
	if got, err := droppedUpstream(dir, base, m1, []string{"b.txt"}); err != nil || got != nil {
		t.Fatalf("droppedUpstream(other path) = %q, %v; want nil", got, err)
	}
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	if got, err := droppedUpstream(dir, base, m1, []string{"a.go"}); err != nil || len(got) != 1 {
		t.Fatalf("droppedUpstream(deleted) = %q, %v; want the one added line", got, err)
	}
}

// TestUpstreamDroppedDiffConfig: a repository's diff prefix config does not
// hide a dropped line from the header parse (issue #941).
func TestUpstreamDroppedDiffConfig(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		dir, base, m1 := upstreamRepo(t)
		git(t, dir, []string{"config", key, "true"})
		git(t, dir, []string{"merge", "-q", "--ff-only", "main"})
		upstreamWrite(t, dir, "a.go", "package x\nvar u = 1\n", "unit drops X")
		got, err := droppedUpstream(dir, base, m1, []string{"a.go"})
		if want := []string{"a.go: var X = 9"}; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("droppedUpstream(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
}

// TestUpstreamDroppedSpacedPath: the tab git ends a spaced header path with is
// not part of the path: a kept line is not reported, a removed one is reported
// under the real name (issue #941).
func TestUpstreamDroppedSpacedPath(t *testing.T) {
	t.Parallel()
	dir, _, _ := upstreamRepo(t)
	git(t, dir, []string{"merge", "-q", "--ff-only", "main"})
	base := upstreamWrite(t, dir, "a b.go", "package x\n", "spaced base")
	m := upstreamWrite(t, dir, "a b.go", "package x\nvar Y = 1\n", "integration adds Y")
	if got, err := droppedUpstream(dir, base, m, []string{"a b.go"}); err != nil || got != nil {
		t.Fatalf("droppedUpstream(kept) = %q, %v; want nil", got, err)
	}
	upstreamWrite(t, dir, "a b.go", "package x\n", "unit drops Y")
	got, err := droppedUpstream(dir, base, m, []string{"a b.go"})
	if want := []string{"a b.go: var Y = 1"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("droppedUpstream(dropped) = %q, %v; want %q", got, err, want)
	}
}

// droppedTask makes a validated-ready T1 owning a.go and u.go, dispatched on
// main's B; M1 on main adds "var X = 9" to a.go; fw/T1 merges main (a merge
// commit after a unit commit to u.go when mergeCommit, else a fast-forward)
// and then drops X. It returns dir.
func droppedTask(t *testing.T, mergeCommit bool) string {
	t.Helper()
	dir, err := initTaskOwns(t, []string{"a.go", "u.go"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	git(t, dir, []string{"branch", "-M", "main"})
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Base: base}); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"checkout", "-q", "-b", "fw/T1"})
	if mergeCommit {
		upstreamWrite(t, dir, "u.go", "package x\n", "unit u.go")
	}
	git(t, dir, []string{"checkout", "-q", "main"})
	upstreamWrite(t, dir, "a.go", "package x\n\nvar X = 9\n", "M1")
	git(t, dir, []string{"checkout", "-q", "fw/T1"})
	if mergeCommit {
		git(t, dir, []string{"merge", "-q", "--no-ff", "--no-edit", "main"})
	} else {
		git(t, dir, []string{"merge", "-q", "--ff-only", "main"})
	}
	upstreamWrite(t, dir, "a.go", "package x\n\nvar u = 1\n", "unit drops X")
	logFinished(t, dir, "T1", "w1")
	return dir
}

// TestValidateDroppedFailsPass: a branch that merged main and dropped the
// line main added fails validate with Dropped set, and the owns_checked event
// carries it, after a fast-forward and after a merge commit (issue #770).
func TestValidateDroppedFailsPass(t *testing.T) {
	t.Parallel()
	for _, mergeCommit := range []bool{false, true} {
		dir := droppedTask(t, mergeCommit)
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask(merge commit %v) error = %v", mergeCommit, err)
		}
		want := []string{"a.go: var X = 9"}
		if res.OK() || !res.GatesOK || !res.OwnsOK || !reflect.DeepEqual(res.Dropped, want) || res.Pending != "" {
			t.Fatalf("ValidateTask(merge commit %v) = OK %v gates %v owns %v dropped %q pending %q, want a failure on %q", mergeCommit, res.OK(), res.GatesOK, res.OwnsOK, res.Dropped, res.Pending, want)
		}
		var owns *Event
		for _, e := range mustEvents(t, dir) {
			if e.Kind == "owns_checked" {
				e := e
				owns = &e
			}
		}
		if owns == nil || !reflect.DeepEqual(owns.Dropped, want) || ownsReadingClean(*owns) {
			t.Fatalf("owns_checked = %+v, want dropped %q and not clean", owns, want)
		}
	}
}

// TestValidateDroppedIgnoresOwnPaths: in a repository tracking flywheel.md, a
// row an integration fix added to the status file and the next derive overwrote
// is not the unit's dropped line, while an owned file's dropped line still fails
// the pass (issue #945).
func TestValidateDroppedIgnoresOwnPaths(t *testing.T) {
	t.Parallel()
	dir, err := initTaskOwns(t, []string{"a.go"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	git(t, dir, []string{"branch", "-M", "main"})
	upstreamWrite(t, dir, ".gitignore", ".flywheel/\n", "track the status file")
	base := upstreamWrite(t, dir, "flywheel.md", "# status\n", "status")
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Base: base}); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"checkout", "-q", "-b", "fw/T1"})
	git(t, dir, []string{"checkout", "-q", "main"})
	upstreamWrite(t, dir, "flywheel.md", "# status\n| merged row |\n", "fix: status row")
	upstreamWrite(t, dir, "a.go", "package x\n\nvar X = 9\n", "fix: add X")
	git(t, dir, []string{"checkout", "-q", "fw/T1"})
	git(t, dir, []string{"merge", "-q", "--ff-only", "main"})
	upstreamWrite(t, dir, "flywheel.md", "# status\n| derived row |\n", "status derived")
	upstreamWrite(t, dir, "a.go", "package x\n\nvar X = 9\nvar u = 1\n", "unit keeps X")
	logFinished(t, dir, "T1", "w1")

	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask(kept) error = %v", err)
	}
	if !res.OK() || len(res.Dropped) != 0 {
		t.Fatalf("ValidateTask(kept) = OK %v dropped %q, want a pass with nothing dropped", res.OK(), res.Dropped)
	}
	for _, fix := range res.FixLines {
		if strings.Contains(fix, "flywheel.md") {
			t.Fatalf("ValidateTask(kept) fix lines = %q, want no flywheel.md entry", res.FixLines)
		}
	}

	upstreamWrite(t, dir, "a.go", "package x\n\nvar u = 1\n", "unit drops X")
	res, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask(dropped) error = %v", err)
	}
	if want := []string{"a.go: var X = 9"}; res.OK() || !reflect.DeepEqual(res.Dropped, want) {
		t.Fatalf("ValidateTask(dropped) = OK %v dropped %q, want a failure on %q", res.OK(), res.Dropped, want)
	}
	for _, fix := range res.FixLines {
		if strings.Contains(fix, "flywheel.md") {
			t.Fatalf("ValidateTask(dropped) fix lines = %q, want no flywheel.md entry", res.FixLines)
		}
	}
}

// TestInspectRefusesDropped: a pass on a reading with dropped integration
// lines is refused under T3, naming them and the rebased escape (issue #770).
func TestInspectRefusesDropped(t *testing.T) {
	t.Parallel()
	dir := droppedTask(t, false)
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass on a reading with dropped upstream lines")
	}
	if got := refusalRule(t, err); got != "T3" || !strings.Contains(err.Error(), "a.go: var X = 9") || !strings.Contains(err.Error(), "--kind rebased") {
		t.Errorf("InspectTask() = %v (rule %s), want a T3 refusal naming a.go: var X = 9 and the rebased escape", err, got)
	}
}

// TestUpstreamPending: an integration commit after the fork touching an
// owned file is named with the file; one touching only an unowned file is
// not (issue #770).
func TestUpstreamPending(t *testing.T) {
	t.Parallel()
	dir, base, m1 := upstreamRepo(t)
	got, err := pendingUpstream(dir, base, "main", []string{"a.go"}, []string{"a.go"})
	if err != nil || !strings.Contains(got, short7(m1)) || !strings.Contains(got, "a.go") || !strings.HasPrefix(got, "1 integration commit(s)") || !strings.HasSuffix(got, "merge main before landing") {
		t.Fatalf("pendingUpstream(owned) = %q, %v; want one commit %s naming a.go", got, err, short7(m1))
	}
	if got, err := pendingUpstream(dir, base, "main", nil, []string{"sub/"}); err != nil || got != "" {
		t.Fatalf("pendingUpstream(unowned) = %q, %v; want none", got, err)
	}
	git(t, dir, []string{"checkout", "-q", "main"})
	upstreamWrite(t, dir, "b.txt", "b2\n", "M2")
	if got, err := pendingUpstream(dir, m1, "main", []string{"a.go"}, []string{"a.go"}); err != nil || got != "" {
		t.Fatalf("pendingUpstream(unowned only) = %q, %v; want none", got, err)
	}
}

// TestUpstreamPendingIgnoresOwnPaths: an integration commit touching only
// flywheel's own bookkeeping is neither named nor counted; a later one
// touching the owned file is (issue #945).
func TestUpstreamPendingIgnoresOwnPaths(t *testing.T) {
	t.Parallel()
	dir, _, m1 := upstreamRepo(t)
	owns := []string{"a.go"}
	git(t, dir, []string{"checkout", "-q", "main"})
	upstreamWrite(t, dir, "flywheel.md", "# plan\n", "M2")
	if got, err := pendingUpstream(dir, m1, "main", nil, owns); err != nil || got != "" {
		t.Fatalf("pendingUpstream(own path only) = %q, %v; want none", got, err)
	}
	m3 := upstreamWrite(t, dir, "a.go", "package x\n\nvar X = 10\n", "M3")
	got, err := pendingUpstream(dir, m1, "main", nil, owns)
	if err != nil || !strings.HasPrefix(got, "1 integration commit(s)") || !strings.Contains(got, "a.go") || !strings.Contains(got, short7(m3)) || strings.Contains(got, "flywheel.md") {
		t.Fatalf("pendingUpstream(owned) = %q, %v; want one commit %s naming a.go and not flywheel.md", got, err, short7(m3))
	}
}
