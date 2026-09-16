package flywheel

import (
	"errors"
	"os"
	"path/filepath"
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
