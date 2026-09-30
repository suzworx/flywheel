package flywheel

import "fmt"

// landedCommit returns task's effective landed commit and tree: the last
// landed or land_corrected event in log order (issue #673). superseded lists
// the commits of the earlier ones, oldest first; commit is "" when the task
// never landed.
func landedCommit(events []Event, task string) (commit, tree string, superseded []string) {
	for _, e := range events {
		if e.Task != task || e.Kind != "landed" && e.Kind != "land_corrected" {
			continue
		}
		if commit != "" {
			superseded = append(superseded, commit)
		}
		commit, tree = e.Commit, e.Tree
	}
	return commit, tree, superseded
}

// CorrectLanding records a land_corrected event (issue #673): task landed
// with the wrong commit, and commit is the one that really merged it. The
// landed event stays in the log; the correction supersedes its commit. It is
// refused (T5) when the task has no landed event, when commit is malformed or
// already the effective landed commit, or when verifyLandCommit refuses it
// (its error passes through, so an InconclusiveError stays exit 8); and (T4)
// when session is a worker session for the task. Reason and session are
// required. It takes the dispatch lock landTask takes.
func CorrectLanding(dir, task, commit, reason, session string) error {
	if dir == "" {
		dir = "."
	}
	if reason == "" || session == "" {
		return fmt.Errorf("a landing correction requires a reason and a session")
	}
	release, err := acquireRepoLock(dir, "dispatch.lock", defaultRepoLockTimings())
	if err != nil {
		return err
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	current, _, _ := landedCommit(events, task)
	if current == "" {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s has no landed event; nothing to correct; land it first: flywheel land %s --commit <sha>", task, task)}
	}
	if !CommitOK(commit) {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("commit %q is not 7 to 40 hex characters", commit)}
	}
	if commit == current {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s already lands with commit %s; there is nothing to correct", task, commit)}
	}
	if workerSessions(events, task)[session] {
		return &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("session %q is a worker session for %s; a landing correction must come from the lead", session, task)}
	}
	if err := verifyLandCommit(dir, task, commit, events); err != nil {
		return err
	}
	ev := Event{Task: task, Kind: "land_corrected", Commit: commit, Tree: landedTree(dir, commit), Note: reason, Session: session}
	if err := AppendEvent(dir, ev); err != nil {
		return fmt.Errorf("append land_corrected for %s: %w", task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return fmt.Errorf("refresh state for %s: %w", task, err)
	}
	return nil
}
