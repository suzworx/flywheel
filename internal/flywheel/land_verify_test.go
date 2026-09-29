package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitFile writes body to dir/name, commits it on the checked-out branch
// and returns the commit: a real commit for a test to land.
func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	shipWrite(t, dir, name, body)
	shipGit(t, dir, "add", "--", name)
	shipGit(t, dir, "commit", "-q", "-m", "change "+name)
	return shipGit(t, dir, "rev-parse", "HEAD")
}

// landVerifyRepo is a repository on main with task U planned owning u.txt,
// dispatched from main's first commit, validated and inspected pass; U's
// work is one commit on the unmerged branch fw/U, returned as unit. With
// tree set the inspection records fw/U's tree, else no tree (the owns
// fallback).
func landVerifyRepo(t *testing.T, tree bool) (dir, unit string) {
	t.Helper()
	dir = t.TempDir()
	shipGit(t, dir, "init", "-q")
	shipGit(t, dir, "config", "gc.auto", "0")
	base := commitFile(t, dir, ".gitignore", ".flywheel/\nflywheel.md\n")
	shipGit(t, dir, "branch", "-M", "main")
	shipGit(t, dir, "checkout", "-q", "-b", "fw/U")
	unit = commitFile(t, dir, "u.txt", "unit\n")
	unitTree := shipGit(t, dir, "rev-parse", "HEAD^{tree}")
	shipGit(t, dir, "checkout", "-q", "main")
	shipWrite(t, dir, ".flywheel/briefs/U.txt", "owns: u.txt\nneeds: none\ngate: true\n\n# TASK: U\n")
	if err := RecordPlanned(dir, "U", ".flywheel/briefs/U.txt"); err != nil {
		t.Fatal(err)
	}
	inspected := ""
	if tree {
		inspected = unitTree
	}
	rc := 0
	if err := AppendEvents(dir, []Event{
		{Task: "U", Kind: "dispatched", Attempt: "r1", Base: base},
		{Task: "U", Kind: "finished", Attempt: "r1", Reason: "stop"},
		{Task: "U", Kind: "validated", Attempt: "r1", Gate: "1", Tree: unitTree, RC: &rc},
		{Task: "U", Kind: "inspected", Attempt: "r1", Verdict: "pass", Session: "lead", Tree: inspected},
	}); err != nil {
		t.Fatal(err)
	}
	return dir, unit
}

// landedAs returns U's landed commit in dir's ledger, "" when none.
func landedAs(t *testing.T, dir string) string {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Task == "U" && e.Kind == "landed" {
			return e.Commit
		}
	}
	return ""
}

// wantT5 fails unless err is a T5 refusal whose fix contains every part.
func wantT5(t *testing.T, err error, parts ...string) {
	t.Helper()
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "T5" {
		t.Fatalf("LandTask = %v; want a T5 refusal", err)
	}
	for _, p := range parts {
		if !strings.Contains(rr.Fix, p) {
			t.Errorf("T5 fix %q lacks %q", rr.Fix, p)
		}
	}
}

// TestLandCommitVerifyUnrelatedRefused: another unit's merged commit on main
// touches none of U's files; landing it is refused naming its subject.
func TestLandCommitVerifyUnrelatedRefused(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, true)
	other := commitFile(t, dir, "other.txt", "refactor\n")
	wantT5(t, LandTask(dir, "U", other, "", false, ""), "change other.txt", "touches none of U's files", "u.txt")
	if c := landedAs(t, dir); c != "" {
		t.Errorf("landed %s after a refusal", c)
	}
}

// TestLandCommitVerifyUnitCommitLands: the merge of fw/U into main touches
// u.txt; it lands and is recorded.
func TestLandCommitVerifyUnitCommitLands(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, true)
	shipGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge U", "fw/U")
	merge := shipGit(t, dir, "rev-parse", "HEAD")
	if err := LandTask(dir, "U", merge, "", false, ""); err != nil {
		t.Fatalf("LandTask(merge) = %v", err)
	}
	if c := landedAs(t, dir); c != merge {
		t.Errorf("landed = %q, want %s", c, merge)
	}
}

// TestLandCommitVerifyMissingInconclusive: a commit id that does not resolve
// is inconclusive (exit 8), and nothing is appended.
func TestLandCommitVerifyMissingInconclusive(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, true)
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = LandTask(dir, "U", "deadbeef00112233", "", false, "")
	var inc *InconclusiveError
	if !errors.As(err, &inc) || !strings.Contains(inc.Fix, "deadbeef00112233") || !strings.Contains(inc.Fix, "git fetch") {
		t.Fatalf("LandTask = %v; want an InconclusiveError naming the commit and git fetch", err)
	}
	if after, _ := ReadEvents(dir); len(after) != len(before) {
		t.Errorf("events %d -> %d; want nothing appended", len(before), len(after))
	}
}

// TestLandCommitVerifySideBranchRefused: U's own commit on fw/U, never
// merged into main, is not on the integration branch.
func TestLandCommitVerifySideBranchRefused(t *testing.T) {
	t.Parallel()
	dir, unit := landVerifyRepo(t, true)
	wantT5(t, LandTask(dir, "U", unit, "", false, ""), "not on the integration branch", "refs/heads/main", "git fetch")
}

// TestLandCommitVerifyOtherRemote (correction c1): with no origin remote and
// the local main behind, U's merge on refs/remotes/upstream/main lands (ship
// --remote upstream fetches there); a commit on no remote's main touching
// u.txt is still refused, naming upstream's ref among those checked.
func TestLandCommitVerifyOtherRemote(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, true)
	shipGit(t, dir, "checkout", "-q", "--detach", "main")
	shipGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge U", "fw/U")
	merge := shipGit(t, dir, "rev-parse", "HEAD")
	shipGit(t, dir, "update-ref", "refs/remotes/upstream/main", merge)
	shipGit(t, dir, "checkout", "-q", "-b", "side", "main")
	side := commitFile(t, dir, "u.txt", "unit, elsewhere\n")
	shipGit(t, dir, "checkout", "-q", "main")
	wantT5(t, LandTask(dir, "U", side, "", false, ""), "not on the integration branch", "refs/remotes/upstream/main", "refs/heads/main")
	if err := LandTask(dir, "U", merge, "", false, ""); err != nil {
		t.Fatalf("LandTask(upstream merge) = %v", err)
	}
	if c := landedAs(t, dir); c != merge {
		t.Errorf("landed = %q, want %s", c, merge)
	}
}

// TestLandCommitVerifyOwnsFallback: with no inspected tree the owns decide:
// a commit on main touching none of them is refused, one touching u.txt lands.
func TestLandCommitVerifyOwnsFallback(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, false)
	other := commitFile(t, dir, "other.txt", "refactor\n")
	wantT5(t, LandTask(dir, "U", other, "", false, ""), "touches none of U's files", "u.txt")
	mine := commitFile(t, dir, "u.txt", "unit, squashed\n")
	if err := LandTask(dir, "U", mine, "", false, ""); err != nil {
		t.Fatalf("LandTask(owned) = %v", err)
	}
	if c := landedAs(t, dir); c != mine {
		t.Errorf("landed = %q, want %s", c, mine)
	}
}

// TestLandCommitVerifyNoRepository: outside a git repository there is
// nothing to verify, and a made-up commit id lands as before.
func TestLandCommitVerifyNoRepository(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	rc := 0
	if err := AppendEvents(dir, []Event{
		{Task: "U", Kind: "planned", Brief: "brief.txt"},
		{Task: "U", Kind: "dispatched", Attempt: "r1"},
		{Task: "U", Kind: "finished", Attempt: "r1", Reason: "stop"},
		{Task: "U", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc},
		{Task: "U", Kind: "inspected", Attempt: "r1", Verdict: "pass", Session: "lead"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := LandTask(dir, "U", "abcdef0", "", false, ""); err != nil {
		t.Fatalf("LandTask outside a repository = %v", err)
	}
	if c := landedAs(t, dir); c != "abcdef0" {
		t.Errorf("landed = %q, want abcdef0", c)
	}
}
