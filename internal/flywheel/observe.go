package flywheel

import (
	"fmt"
	"time"
)

// Observe records one check only a person can make (issue #842): the
// brief's observe line number check (1-based), looked at by the lead, passed
// or failed. It appends one validated event with Gate obs<n>, the observe
// line as its Command, the unit's current tree (the tree flywheel validate
// hashes: the recorded workdir, else dir), RC 0 for a pass and 1 for a fail,
// Source "observed", the evidence (a photo or reference) and the session, so
// a later `flywheel inspect` pass finds it on that tree (T3).
//
// It is refused, as Attest is, without evidence (an error), without a
// session or from a worker session of the task (T4), and before the task has
// a dispatched attempt (T5); a check outside 1..len(Observes) is an error
// naming the valid range. It returns the observed tree.
func Observe(dir, task string, check int, pass bool, evidence, session string) (string, error) {
	if dir == "" {
		dir = "."
	}
	if evidence == "" {
		return "", fmt.Errorf("observe %s: --evidence is required (the photo or reference of what was seen)", task)
	}
	if session == "" {
		return "", &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("an observation for %s requires the lead's --session", task)}
	}
	release, err := acquireRepoLock(dir, "dispatch.lock", defaultRepoLockTimings())
	if err != nil {
		return "", err
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return "", err
	}
	if workerSessions(events, task)[session] {
		return "", &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("session %q is a worker session for %s; an observation must come from the lead", session, task)}
	}
	_, attempt, _ := latestBaseBriefAndAttempt(events, task)
	if attempt == "" {
		return "", &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s has no dispatched attempt; only a dispatched unit can be observed", task)}
	}
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return "", fmt.Errorf("observe %s: %w", task, err)
	}
	if len(header.Observes) == 0 {
		return "", fmt.Errorf("observe %s: the brief declares no observe: lines", task)
	}
	if check < 1 || check > len(header.Observes) {
		return "", fmt.Errorf("observe %s: --check %d is outside 1..%d, the brief's observe: lines", task, check, len(header.Observes))
	}
	tree, err := hashTree(absPath(wd("", dir, events, task)))
	if err != nil {
		return "", fmt.Errorf("observe %s: hash the unit's tree: %w", task, err)
	}
	rc := 1
	if pass {
		rc = 0
	}
	if err := AppendEvent(dir, Event{
		Task: task, Kind: "validated", Attempt: attempt, Gate: fmt.Sprintf("obs%d", check),
		Command: header.Observes[check-1], Tree: tree, RC: &rc, Session: session,
		Source: "observed", Evidence: evidence, Persona: "supervisor",
	}); err != nil {
		return "", fmt.Errorf("append observation for %s: %w", task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return "", fmt.Errorf("refresh state for %s: %w", task, err)
	}
	return tree, nil
}

// hasPassingObservation reports whether task's latest sound observation of
// observe line n on tree after after passed (issue #842): a failing
// observation is not a pass, and a later failure overrules an earlier pass.
func hasPassingObservation(events []Event, task string, n int, tree string, after time.Time) bool {
	gate := fmt.Sprintf("obs%d", n)
	passed := false
	for _, e := range events {
		if e.Task != task || e.Kind != "validated" || e.Gate != gate || e.Tree != tree {
			continue
		}
		if e.Source != "observed" || !externalReadingOK(e) || e.RC == nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(after) {
			passed = *e.RC == 0
		}
	}
	return passed
}
