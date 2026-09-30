package flywheel

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// wrongLanding is landVerifyRepo with U landed (appended directly, the way a
// landing before part a recorded it) on an unrelated commit on main. It
// returns the wrong commit and the merge of fw/U into main.
func wrongLanding(t *testing.T) (dir, wrong, merge string) {
	t.Helper()
	dir, _ = landVerifyRepo(t, true)
	wrong = commitFile(t, dir, "other.txt", "refactor\n")
	if err := AppendEvent(dir, Event{Task: "U", Kind: "landed", Commit: wrong}); err != nil {
		t.Fatal(err)
	}
	shipGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge U", "fw/U")
	return dir, wrong, shipGit(t, dir, "rev-parse", "HEAD")
}

// wantRule fails unless err is a refusal of rule whose fix contains every part.
func wantRule(t *testing.T, err error, rule string, parts ...string) {
	t.Helper()
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != rule {
		t.Fatalf("err = %v; want a %s refusal", err, rule)
	}
	for _, p := range parts {
		if !strings.Contains(rr.Fix, p) {
			t.Errorf("%s fix %q lacks %q", rule, rr.Fix, p)
		}
	}
}

// TestLandCorrectToUnitCommit: correcting the wrong landing to U's merge is
// recorded, is the effective commit, and T4/T5 stay clean; re-landing the
// corrected commit is a no-op and any other commit is refused (T5).
func TestLandCorrectToUnitCommit(t *testing.T) {
	t.Parallel()
	dir, wrong, merge := wrongLanding(t)
	if err := CorrectLanding(dir, "U", merge, "landed the wrong PR", "lead"); err != nil {
		t.Fatalf("CorrectLanding = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	commit, tree, superseded := landedCommit(events, "U")
	if commit != merge || tree == "" || !reflect.DeepEqual(superseded, []string{wrong}) {
		t.Errorf("landedCommit = %q, %q, %v; want %s, a tree, [%s]", commit, tree, superseded, merge, wrong)
	}
	for _, it := range append(ruleT4("U", events), ruleT5("U", events)...) {
		if !it.Pass {
			t.Errorf("%s failed after a correction: %s", it.Rule, it.Reason)
		}
	}
	if st := Derive(events); st.Tasks[len(st.Tasks)-1].Status != "landed" {
		t.Errorf("status = %q, want landed", st.Tasks[len(st.Tasks)-1].Status)
	}
	if err := LandTask(dir, "U", merge, "", false, ""); !errors.Is(err, ErrAlreadyLanded) {
		t.Errorf("re-land corrected = %v; want ErrAlreadyLanded", err)
	}
	wantRule(t, LandTask(dir, "U", wrong, "", false, ""), "T5", "already landed with commit "+merge)
	wantRule(t, CorrectLanding(dir, "U", merge, "again", "lead"), "T5", "nothing to correct")
}

// TestLandCorrectUnrelatedRefused: another unrelated commit is refused by
// verifyLandCommit, naming its subject; nothing is appended.
func TestLandCorrectUnrelatedRefused(t *testing.T) {
	t.Parallel()
	dir, _, _ := wrongLanding(t)
	other := commitFile(t, dir, "else.txt", "x\n")
	before, _ := ReadEvents(dir)
	wantRule(t, CorrectLanding(dir, "U", other, "wrong again", "lead"), "T5", "change else.txt", "touches none of U's files")
	if after, _ := ReadEvents(dir); len(after) != len(before) {
		t.Errorf("events %d -> %d; want nothing appended", len(before), len(after))
	}
}

// TestLandCorrectNotLandedRefused: a task with no landed event has nothing
// to correct.
func TestLandCorrectNotLandedRefused(t *testing.T) {
	t.Parallel()
	dir, _ := landVerifyRepo(t, true)
	shipGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge U", "fw/U")
	merge := shipGit(t, dir, "rev-parse", "HEAD")
	wantRule(t, CorrectLanding(dir, "U", merge, "why", "lead"), "T5", "nothing to correct", "land it first")
}

// TestLandCorrectWorkerSessionRefused: a worker session cannot correct (T4).
func TestLandCorrectWorkerSessionRefused(t *testing.T) {
	t.Parallel()
	dir, _, merge := wrongLanding(t)
	if err := AppendEvent(dir, Event{Task: "U", Kind: "report", Attempt: "r1", Session: "w1", Note: "done"}); err != nil {
		t.Fatal(err)
	}
	wantRule(t, CorrectLanding(dir, "U", merge, "why", "w1"), "T4", "worker session")
}

// TestLandCorrectLandedCommit: the last landed or land_corrected commit is
// effective; the earlier ones are superseded, in log order.
func TestLandCorrectLandedCommit(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Task: "U", Kind: "landed", Commit: "aaaaaaa", Tree: "t1"},
		{Task: "V", Kind: "landed", Commit: "ccccccc"},
		{Task: "U", Kind: "land_corrected", Commit: "bbbbbbb", Tree: "t2", Note: "n", Session: "lead"},
	}
	c, tree, sup := landedCommit(events, "U")
	if c != "bbbbbbb" || tree != "t2" || !reflect.DeepEqual(sup, []string{"aaaaaaa"}) {
		t.Errorf("landedCommit = %q, %q, %v", c, tree, sup)
	}
	if c, _, sup := landedCommit(events, "W"); c != "" || sup != nil {
		t.Errorf("landedCommit(W) = %q, %v; want none", c, sup)
	}
}

// TestLandCorrectExplainWhyLanded: explain and why-landed name the corrected
// commit; explain also shows the superseded one.
func TestLandCorrectExplainWhyLanded(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-29T10:00:00Z", Task: "U", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-29T10:01:00Z", Task: "U", Kind: "landed", Commit: "aaaaaaa1"},
		{TS: "2026-09-29T10:02:00Z", Task: "U", Kind: "land_corrected", Commit: "bbbbbbb2", Tree: "t2", Note: "wrong PR", Session: "lead"},
	}
	x, err := Explain(events, "U")
	if err != nil {
		t.Fatal(err)
	}
	if x.Commit != "bbbbbbb2" || x.Tree != "t2" || !reflect.DeepEqual(x.Superseded, []string{"aaaaaaa1"}) {
		t.Errorf("Explain = commit %q tree %q superseded %v", x.Commit, x.Tree, x.Superseded)
	}
	var b strings.Builder
	if err := RenderExplanation(&b, x); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- landed: bbbbbbb2", "superseded aaaaaaa1", "landing corrected to bbbbbbb2 by lead", "wrong PR"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("explain output lacks %q:\n%s", want, b.String())
		}
	}
	if got := whyLanded(events, "U"); !strings.Contains(got, "as bbbbbbb") {
		t.Errorf("whyLanded = %q; want the corrected commit", got)
	}
}
