package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixLinesRepo makes a repo with a.txt holding base, committed as "base".
func fixLinesRepo(t *testing.T, base string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	upstreamWrite(t, dir, "a.txt", base, "base")
	return dir
}

// fixLinesEdit overwrites name in dir's working tree without committing.
func fixLinesEdit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixLinesWant asserts removedFixLines from HEAD over paths returns want.
func fixLinesWant(t *testing.T, dir string, paths []string, want []string) {
	t.Helper()
	got, err := removedFixLines(dir, git(t, dir, []string{"rev-parse", "HEAD"}), paths)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("removedFixLines(%q) = %q, %v; want %q", paths, got, err, want)
	}
}

// TestFixLinesFixNotFeat: a removed line a fix added is reported with the
// commit's sha12, subject and count; one a feat added is not (issue #940).
func TestFixLinesFixNotFeat(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "keep\n")
	sha := upstreamWrite(t, dir, "a.txt", "keep\nstale\n", "fix: stop stale times")
	upstreamWrite(t, dir, "a.txt", "keep\nstale\nfeature\n", "feat: x")
	fixLinesEdit(t, dir, "a.txt", "keep\nstale\n")
	fixLinesWant(t, dir, []string{"a.txt"}, nil)
	fixLinesEdit(t, dir, "a.txt", "keep\n")
	fixLinesWant(t, dir, []string{"a.txt"}, []string{"a.txt: 1 line(s) from fix " + sha[:12] + " fix: stop stale times"})
}

// TestFixLinesDiffPrefixConfig: a repository's diff prefix settings change the
// headers the parser reads, and must not silently empty the reading.
func TestFixLinesDiffPrefixConfig(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"diff.noprefix", "diff.mnemonicPrefix"} {
		dir := fixLinesRepo(t, "keep\n")
		sha := upstreamWrite(t, dir, "a.txt", "keep\nstale\n", "fix: stop stale times")
		git(t, dir, []string{"config", key, "true"})
		fixLinesEdit(t, dir, "a.txt", "keep\n")
		fixLinesWant(t, dir, []string{"a.txt"}, []string{"a.txt: 1 line(s) from fix " + sha[:12] + " fix: stop stale times"})
	}
}

// TestFixLinesSubjects: only a subject starting with a fix word counts.
func TestFixLinesSubjects(t *testing.T) {
	t.Parallel()
	for subject, counts := range map[string]bool{
		"fix(scope): y": true, "Fix y": true, "fixes: y": true, "FIXED y": true, "hotfix: y": true, "bugfix: y": true,
		"prefix: y": false, "fixture: y": false, "feat: y": false,
	} {
		dir := fixLinesRepo(t, "keep\n")
		upstreamWrite(t, dir, "a.txt", "keep\nline\n", subject)
		fixLinesEdit(t, dir, "a.txt", "keep\n")
		got, err := removedFixLines(dir, "HEAD", []string{"a.txt"})
		if err != nil || (len(got) == 1) != counts || (counts && !strings.HasSuffix(got[0], " "+subject)) {
			t.Errorf("removedFixLines(%q) = %q, %v; counts = %v", subject, got, err, counts)
		}
	}
}

// TestFixLinesNothingRemoved: a CRLF-only change, a pure addition, no paths and
// an empty from report nothing; the last two start no git process, so a
// directory that is no repo cannot fail them.
func TestFixLinesNothingRemoved(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "keep\n")
	upstreamWrite(t, dir, "a.txt", "keep\nline\n", "fix: y")
	fixLinesEdit(t, dir, "a.txt", "keep\r\nline\r\n")
	fixLinesWant(t, dir, []string{"a.txt"}, nil)
	fixLinesEdit(t, dir, "a.txt", "keep\nline\nmore\n")
	fixLinesWant(t, dir, []string{"a.txt"}, nil)
	norepo := t.TempDir()
	if got, err := removedFixLines(norepo, "HEAD", nil); got != nil || err != nil {
		t.Fatalf("removedFixLines(no paths) = %q, %v", got, err)
	}
	if got, err := removedFixLines(norepo, "", []string{"a.txt"}); got != nil || err != nil {
		t.Fatalf("removedFixLines(empty from) = %q, %v", got, err)
	}
	if _, err := removedFixLines(norepo, "HEAD", []string{"a.txt"}); err == nil {
		t.Fatal("removedFixLines(no repo) = nil error, want the git error")
	}
}

// TestFixLinesDeletedAndRenamed: a deleted file reports its fix lines, also
// under a path holding a space; a renamed, otherwise unchanged file does not.
func TestFixLinesDeletedAndRenamed(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gone.txt", "gone too.txt"} {
		dir := fixLinesRepo(t, "keep\n")
		sha := upstreamWrite(t, dir, name, "one\ntwo\n", "fix: y")
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		fixLinesWant(t, dir, []string{name}, []string{name + ": 2 line(s) from fix " + sha[:12] + " fix: y"})
	}
	dir := fixLinesRepo(t, "keep\n")
	upstreamWrite(t, dir, "old.txt", "one\ntwo\n", "fix: y")
	from := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"mv", "old.txt", "new.txt"})
	if got, err := removedFixLines(dir, from, []string{"old.txt", "new.txt"}); got != nil || err != nil {
		t.Fatalf("removedFixLines(renamed) = %q, %v; want nothing", got, err)
	}
}

// TestFixLinesBlank: a blank line a fix added and the unit removed is skipped.
func TestFixLinesBlank(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "keep\nmore\n")
	upstreamWrite(t, dir, "a.txt", "keep\n\nmore\n", "fix: y")
	fixLinesEdit(t, dir, "a.txt", "keep\nmore\n")
	fixLinesWant(t, dir, []string{"a.txt"}, nil)
}

// TestFixLinesCounts: a file with two hunks, one of them touching lines of two
// fix commits, counts per commit.
func TestFixLinesCounts(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "k1\nk2\nk3\nk4\nk5\n")
	one := upstreamWrite(t, dir, "a.txt", "k1\none1\none2\nk2\nk3\nk4\nk5\n", "fix: one")
	two := upstreamWrite(t, dir, "a.txt", "k1\none1\none2\ntwo1\nk2\nk3\nk4\nk5\ntwo2\n", "Fix two")
	fixLinesEdit(t, dir, "a.txt", "k1\nk2\nk3\nk4\nk5\n")
	want := []string{"a.txt: 2 line(s) from fix " + one[:12] + " fix: one", "a.txt: 2 line(s) from fix " + two[:12] + " Fix two"}
	sort.Strings(want)
	fixLinesWant(t, dir, []string{"a.txt"}, want)
}

// TestFixLinesHeaderShownOnce: porcelain prints a commit's summary only on its
// first appearance, so later lines still count, and content that reads like a
// header or a summary stays content. A real blame covers two -L ranges.
func TestFixLinesHeaderShownOnce(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "k1\nk2\nk3\n")
	sha := upstreamWrite(t, dir, "a.txt", "one\nk1\nk2\nk3\ntwo\n", "fix: y")
	fixLinesEdit(t, dir, "a.txt", "k1\nk2\nk3\n")
	fixLinesWant(t, dir, []string{"a.txt"}, []string{"a.txt: 2 line(s) from fix " + sha[:12] + " fix: y"})

	aaa, bbb := strings.Repeat("a", 40), strings.Repeat("b", 40)
	out := aaa + " 1 1 1\nauthor x\nsummary feat: z\nfilename a.txt\n\tsummary fix: fake\n" +
		bbb + " 2 2 2\nauthor summary y\nsummary " + strings.Repeat("s", 90) + "\nfilename a.txt\n\t" + bbb + " 9 9 9\n" +
		bbb + " 3 3\n\tsecond\n" + aaa + " 4 4\n\tnot a fix\n" + bbb + " 5 5\n\t  \n"
	if got := blameFixEntries("a.txt", out); len(got) != 0 {
		t.Errorf("blameFixEntries(no fix subject) = %q, want none", got)
	}
	out = strings.Replace(out, "summary sss", "summary fix ", 1)
	want := []string{"a.txt: 2 line(s) from fix " + bbb[:12] + " fix " + strings.Repeat("s", 76)}
	if got := blameFixEntries("a.txt", out); !reflect.DeepEqual(got, want) {
		t.Errorf("blameFixEntries() = %q, want %q", got, want)
	}
}

// TestFixLinesMergedRemoval: a line integration removed and the branch merged
// is not the unit's; from the merged commit only the unit's own removal counts,
// and the reading reaches owns_checked and explain while leaving the pass clean.
func TestFixLinesMergedRemoval(t *testing.T) {
	t.Parallel()
	dir, err := initTaskOwns(t, []string{"a.go", "u.go"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	git(t, dir, []string{"branch", "-M", "main"})
	fix := upstreamWrite(t, dir, "a.go", "package x\n\nvar Y = 1\n\nvar Z = 2\n", "fix: stop stale times")
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Base: fix}); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"checkout", "-q", "-b", "fw/T1"})
	upstreamWrite(t, dir, "u.go", "package x\n", "unit u.go")
	git(t, dir, []string{"checkout", "-q", "main"})
	merged := upstreamWrite(t, dir, "a.go", "package x\n\nvar Y = 1\n", "M1 drops Z")
	git(t, dir, []string{"checkout", "-q", "fw/T1"})
	git(t, dir, []string{"merge", "-q", "--no-ff", "--no-edit", "main"})
	upstreamWrite(t, dir, "a.go", "package x\n", "unit drops Y")
	logFinished(t, dir, "T1", "w1")

	entry := func(n int) string {
		return fmt.Sprintf("a.go: %d line(s) from fix %s fix: stop stale times", n, fix[:12])
	}
	if got, err := removedFixLines(dir, merged, []string{"a.go"}); err != nil || !reflect.DeepEqual(got, []string{entry(1)}) {
		t.Fatalf("removedFixLines(merged) = %q, %v; want only Y", got, err)
	}
	if got, err := removedFixLines(dir, fix, []string{"a.go"}); err != nil || !reflect.DeepEqual(got, []string{entry(2)}) {
		t.Fatalf("removedFixLines(base) = %q, %v; want Y and Z", got, err)
	}

	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := []string{entry(1)}
	if !res.OK() || !res.GatesOK || !res.OwnsOK || len(res.Markers) != 0 || len(res.Dropped) != 0 || !reflect.DeepEqual(res.FixLines, want) {
		t.Fatalf("ValidateTask() = %+v, want OK with fix lines %q", res, want)
	}
	var owns *Event
	for _, e := range mustEvents(t, dir) {
		if e.Kind == "owns_checked" {
			e := e
			owns = &e
		}
	}
	if owns == nil || !reflect.DeepEqual(owns.FixLines, want) || !ownsReadingClean(*owns) {
		t.Fatalf("owns_checked = %+v, want fix lines %q and clean", owns, want)
	}
	if got, wantLine := explainLine(*owns), "owns check: ok; removes fix lines: "+entry(1); got != wantLine {
		t.Errorf("explainLine(ok) = %q, want %q", got, wantLine)
	}
	if got, wantLine := explainLine(Event{Kind: "owns_checked", Outside: []string{"o.go"}, FixLines: []string{"x", "y"}}), "owns check: outside o.go; removes fix lines: x, y"; got != wantLine {
		t.Errorf("explainLine(outside) = %q, want %q", got, wantLine)
	}
}

// TestFixLinesCap: more than 20 entries gives 20 plus "... and N more".
func TestFixLinesCap(t *testing.T) {
	t.Parallel()
	dir := fixLinesRepo(t, "keep\n")
	var paths []string
	for i := 0; i < maxMarkerEntries+2; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		fixLinesEdit(t, dir, name, "line\n")
		paths = append(paths, name)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-q", "-m", "fix: y"})
	for _, p := range paths {
		if err := os.Remove(filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := removedFixLines(dir, "HEAD", paths)
	if err != nil || len(got) != maxMarkerEntries+1 || got[maxMarkerEntries] != "... and 2 more" {
		t.Fatalf("removedFixLines(cap) = %q, %v", got, err)
	}
}
