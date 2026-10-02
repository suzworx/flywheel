package flywheel

import (
	"slices"
	"time"
)

// ciFailedEvent builds the ci_failed event for task (issue #776): CI failed on
// the unit's PR after every gate in its brief passed. Gates are the gate
// commands of the task's effective brief (AttemptBrief) at this moment, so a
// later pass can tell whether the brief gained a gate since. note names the
// PR and its failing checks.
func ciFailedEvent(dir string, events []Event, task, attempt, note string, now time.Time) (Event, error) {
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return Event{}, err
	}
	return Event{TS: now.UTC().Format(time.RFC3339Nano), Task: task, Kind: "ci_failed", Attempt: attempt,
		Note: note, Gates: slices.Clone(header.Gates)}, nil
}

// CIFailedEvent builds the ci_failed event `flywheel log --kind ci_failed`
// records for a PR merged outside ship: ciFailedEvent over dir's log.
func CIFailedEvent(dir, task, attempt, note string, now time.Time) (Event, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return Event{}, err
	}
	return ciFailedEvent(dir, events, task, attempt, note, now)
}

// recordCIFailed appends ship's ci_failed event for the run's attempt, note
// "#<n>: <failing checks>", its gates from the brief the tree passed.
func (r *shipRun) recordCIFailed(note string) error {
	e, err := ciFailedEvent(r.dir, r.events, r.task, r.attempt, note, r.o.Now())
	if err != nil {
		return err
	}
	return AppendEvent(r.dir, e)
}

// latestCIFailed returns task's newest ci_failed event in derivation order,
// and false when it has none.
func latestCIFailed(events []Event, task string) (Event, bool) {
	var last Event
	found := false
	for _, e := range derivationOrder(events) {
		if e.Task == task && e.Kind == "ci_failed" {
			last, found = e, true
		}
	}
	return last, found
}

// ciEscapeRefusal refuses a pass of task while its newest ci_failed event's
// gates still cover every gate in header (issue #776): CI proved a defect the
// brief's gates let through, and the correction must add a gate that
// reproduces it. nil when the task has no ci_failed event or header carries a
// gate command that event's Gates lacks. Not configurable.
func ciEscapeRefusal(events []Event, task string, header BriefHeader) *RuleRefusal {
	e, ok := latestCIFailed(events, task)
	if !ok {
		return nil
	}
	for _, g := range header.Gates {
		if !slices.Contains(e.Gates, g) {
			return nil
		}
	}
	return &RuleRefusal{Rule: "ci-escape", Fix: "CI failed on " + e.Note +
		" after every gate in the brief passed; add a gate that reproduces it to the brief (flywheel run " +
		task + " --delta ...), then validate again"}
}
