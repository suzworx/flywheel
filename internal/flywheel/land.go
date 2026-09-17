package flywheel

import (
	"errors"
	"fmt"
)

// ErrAlreadyLanded is wrapped in the error LandTask returns when the task was
// already landed with the same commit: landing is a no-op and the caller
// reports success.
var ErrAlreadyLanded = errors.New("already landed")

// CommitOK reports whether s looks like a commit id: 7 to 40 hex characters.
func CommitOK(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// LandTask records a landed event for task after enforcing T5: only a task
// whose derived status is passed may land, and an already-landed task may only
// repeat its recorded commit. On success the landed event is appended and the
// derived state refreshed.
//
// When leadImplemented is set, the landing is recorded as lead-implemented
// (the flag on the landed event) and the reason is put in the event's note,
// prefixed "lead-implemented: "; an operator-supplied note, when present, is
// kept first and the reason appended after "; " (issue #198).
func LandTask(dir, task, commit, note string, leadImplemented bool, reason string) error {
	if dir == "" {
		dir = "."
	}
	if !CommitOK(commit) {
		return fmt.Errorf("commit %q is not 7 to 40 hex characters", commit)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	landed := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "landed" {
			landed = e.Commit
		}
	}
	if landed != "" {
		if landed == commit {
			return fmt.Errorf("%s already landed %s: %w", task, commit, ErrAlreadyLanded)
		}
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s already landed with commit %s; refusing to re-land with %s", task, landed, commit)}
	}
	status := ""
	for _, t := range Derive(events).Tasks {
		if t.ID == task {
			status = t.Status
			break
		}
	}
	if status != "passed" {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s is not passed (status %q); land only after a passing inspection: flywheel inspect %s --verdict pass --session <session>", task, status, task)}
	}
	if leadImplemented {
		suffix := "lead-implemented: " + reason
		if note != "" {
			note = note + "; " + suffix
		} else {
			note = suffix
		}
	}
	if err := AppendEvent(dir, Event{Task: task, Kind: "landed", Commit: commit, Note: note, LeadImplemented: leadImplemented}); err != nil {
		return fmt.Errorf("append landed for %s: %w", task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return fmt.Errorf("refresh state for %s: %w", task, err)
	}
	return nil
}
