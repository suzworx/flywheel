package flywheel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logFinished records a finished event for a task with a worker session.
func logFinished(t *testing.T, dir, task, session string) {
	t.Helper()
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: task, Kind: "finished", Attempt: "r1", Session: session, RC: &rc}); err != nil {
		t.Fatalf("logFinished() error = %v", err)
	}
}

// refusalRule returns the rule id carried by err, or "".
func refusalRule(t *testing.T, err error) string {
	t.Helper()
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a RuleRefusal", err)
	}
	return r.Rule
}

func TestInspectPassRefusedWithoutReadings(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass with no validated readings")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
}

func TestInspectPassRefusedWhenTreeChanged(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nchanged\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass after the tree changed")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
}

func TestInspectRefusedFromWorkerSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "w1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass from a worker session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestInspectRefusedBadVerdict(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "bogus", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a bogus verdict")
	}
	if got := refusalRule(t, err); got != "T8" {
		t.Errorf("rule = %q, want T8", got)
	}
}

func TestInspectAcceptedWhenAllHold(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1", Note: "looks good"})
	if err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "inspected" {
			found = true
			if e.Verdict != "pass" || e.Session != "i1" || e.Persona != "inspector" {
				t.Errorf("inspected event = %+v", e)
			}
			if e.Note != "looks good" {
				t.Errorf("inspected note = %q, want %q", e.Note, "looks good")
			}
		}
	}
	if !found {
		t.Fatal("no inspected event recorded")
	}
}

// TestInspectWorkerSessionPriorityOverReadings checks rule order: a worker
// session is refused as T4 even when the pass's readings are also missing, so
// an inspection from a worker session never gets a T3 message.
func TestInspectWorkerSessionPriorityOverReadings(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "w1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass from a worker session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4 (worker session refused before missing readings)", got)
	}
}

// TestInspectMatchesValidateWorkdir checks that inspect hashes the same
// --workdir tree as validate: with Workdir == Workdir != Dir the trees match
// and the pass is accepted.
func TestInspectMatchesValidateWorkdir(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wd, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() workdir error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wd}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Workdir: wd, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v (workdir tree should match validate)", err)
	}
}

// TestInspectRecordsWorkdirWhenExternal checks the provenance field (issue
// #244): an inspected event records the workdir it measured when that differs
// from the flywheel root, and a same-dir inspection records none.
func TestInspectRecordsWorkdirWhenExternal(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() same-dir error = %v", err)
	}
	wd, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() workdir error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Workdir: wd, Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() external error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count, withWorkdir := 0, 0
	for _, e := range evs {
		if e.Kind == "inspected" {
			count++
			if e.Workdir == wd {
				withWorkdir++
			} else if e.Workdir != "" {
				t.Errorf("inspected workdir = %q, want %q", e.Workdir, wd)
			}
		}
	}
	if count != 2 {
		t.Fatalf("inspected events = %d, want 2", count)
	}
	if withWorkdir != 1 {
		t.Errorf("inspected events carrying the external workdir = %d, want exactly 1", withWorkdir)
	}
}

// TestInspectPassRefusedWithoutLiveReading checks that a brief declaring a
// live-gate is refused T3 on a pass verdict when only the ordinary gates
// have a validated reading (issue #152).
func TestInspectPassRefusedWithoutLiveReading(t *testing.T) {
	dir, err := initTaskLive(t, []string{"exit 0"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskLive() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass with no live reading")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
}

// TestInspectPassAcceptedWithLiveReading checks that the same brief passes
// once a passing live reading exists on the same tree, from a single
// Live: true validate pass (issue #152).
func TestInspectPassAcceptedWithLiveReading(t *testing.T) {
	dir, err := initTaskLive(t, []string{"exit 0"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskLive() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Live: true}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
}

// TestInspectReworkThenPassSameSession checks that an inspector session is
// never mistaken for a worker session: after a rework, the same inspector can
// pass with the same session once the readings hold.
func TestInspectReworkThenPassSameSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() rework error = %v", err)
	}
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() pass with the same inspector session refused: %v", err)
	}
}

// TestInspectPassOnOtherTreeOutsideOwns checks the issue #218 relaxation: a
// reading on tree T, then a change to a file the unit does not own — the pass
// is accepted and the inspected note names T.
func TestInspectPassOnOtherTreeOutsideOwns(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	measured, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	want := "reading from tree " + measured + " (diff outside owns)"
	for _, e := range evs {
		if e.Kind == "inspected" && !strings.Contains(e.Note, want) {
			t.Errorf("inspected note = %q, want it to name the measured tree %s", e.Note, measured)
		}
	}
}

// TestInspectPassRefusedWhenOwnedFileChanged is the regression guard for the
// issue #218 relaxation: a change to a file the unit does own must be refused
// with today's T3 message, never excused.
func TestInspectPassRefusedWhenOwnedFileChanged(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nchanged\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	current, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass after an owned file changed")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
	want := fmt.Sprintf("no passing supervisor validated reading for gate 1 on tree %s after the latest finished event; run: flywheel validate T1", current)
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a RuleRefusal", err)
	}
	if got := r.Fix; got != want {
		t.Errorf("T3 fix = %q, want today's message %q", got, want)
	}
}

// TestInspectCurrentTreeNoteKeepsOperatorNote checks that a reading on the
// current tree still passes with no relaxation suffix: the note is exactly the
// operator's.
func TestInspectCurrentTreeNoteKeepsOperatorNote(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1", Note: "looks good"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "inspected" && e.Note != "looks good" {
			t.Errorf("inspected note = %q, want %q with no relaxation suffix", e.Note, "looks good")
		}
	}
}

// TestInspectPassRefusedSplitAcrossTrees checks that readings split across two
// different trees are refused: no single tree holds every gate and the clean
// owns_checked, so neither can be the tree the pass is evidence about.
func TestInspectPassRefusedSplitAcrossTrees(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\none\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	ta, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	tb, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: ta, RC: &rc}); err != nil {
		t.Fatalf("AppendEvent() validated error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tb}); err != nil {
		t.Fatalf("AppendEvent() owns_checked error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\nthree\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass with readings split across two trees")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
}

// TestInspectRecordsTheMeasuredTree is the regression guard for issue #241: a
// pass must record the tree the T3 readings were proved against, never a
// second hash taken later. The hashTree seam (a package variable added for
// this test and its siblings) lets the test mutate an owned file between the
// readings check and the event write; the recorded tree must still be the
// measured tree and flywheel verify must accept the pass.
func TestInspectRecordsTheMeasuredTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	measured, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	orig := hashTree
	hashTree = func(wd string) (string, error) {
		tree, err := orig(wd)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(wd, "a.go"), []byte("package x\nchanged after measurement\n"), 0o644); err != nil {
			return "", err
		}
		return tree, nil
	}
	defer func() { hashTree = orig }()
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	hashTree = orig
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	got := ""
	for _, e := range evs {
		if e.Kind == "inspected" {
			got = e.Tree
		}
	}
	if got == "" {
		t.Fatal("no inspected event recorded")
	}
	if got != measured {
		t.Errorf("inspected tree = %q, want the measured tree %q", got, measured)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		t.Errorf("VerifyTasks() did not accept the pass: %+v", res.Items)
	}
}

// TestInspectPassRecordsCurrentTree checks the golden behaviour: a plain pass
// with no interleaving records the same tree as the one the readings were
// proved against, exactly as before issue #241.
func TestInspectPassRecordsCurrentTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "inspected" && e.Tree != tree {
			t.Errorf("inspected tree = %q, want the current tree %q", e.Tree, tree)
		}
	}
}

// TestInspectPassFromOtherTreeRecordsCurrentTree checks that a pass granted
// on a different tree T still records the current tree (the one inspected)
// and still names T in the note.
func TestInspectPassFromOtherTreeRecordsCurrentTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	measured, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	current, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	want := "reading from tree " + measured + " (diff outside owns)"
	for _, e := range evs {
		if e.Kind == "inspected" {
			if e.Tree != current {
				t.Errorf("inspected tree = %q, want the current tree %q", e.Tree, current)
			}
			if !strings.Contains(e.Note, want) {
				t.Errorf("inspected note = %q, want it to name the measured tree %s", e.Note, measured)
			}
		}
	}
}

// TestInspectReworkRecordsCurrentTree checks that a rework needs no readings
// and records the current tree, hashed once on its own path.
func TestInspectReworkRecordsCurrentTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "inspected" {
			if e.Tree != tree {
				t.Errorf("inspected tree = %q, want the current tree %q", e.Tree, tree)
			}
			if e.Note != "" {
				t.Errorf("inspected note = %q, want empty", e.Note)
			}
		}
	}
}

// TestInspectPassRefusedByLaterFinished checks that a finished event after the
// reading still invalidates it, exactly as before the issue #218 relaxation.
func TestInspectPassRefusedByLaterFinished(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-30T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "r2", Session: "w1", RC: &rc}); err != nil {
		t.Fatalf("AppendEvent() finished error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass after a later finished event")
	}
	if got := refusalRule(t, err); got != "T3" {
		t.Errorf("rule = %q, want T3", got)
	}
}
