package flywheel

import (
	"fmt"
	"strings"
)

// Attest records gate readings measured outside flywheel (issue #367): a
// named external run — CI on the unit's PR, say — measured commit, and every
// gate passed there. It appends, in one write, a passing validated event per
// gate and live gate of the unit's effective header and a clean owns_checked,
// each on the commit's tree (landedTree's normalisation) with Source
// "external", the evidence, the commit and the attesting session, so a later
// `flywheel inspect --commit` passes T3 on exactly that tree.
//
// An attestation vouches for the unit's own change only: it is refused (T3)
// when the commit changed any path outside the header's owns against its
// first parent. It must come from the lead (T4: a worker session of the task
// is refused), the task must have a dispatched attempt (T5), and the commit
// must be in this repository. It returns the attested tree.
func Attest(dir, task, commit, evidence, session string) (string, error) {
	if dir == "" {
		dir = "."
	}
	if evidence == "" {
		return "", fmt.Errorf("attest %s: --evidence is required (the URL or reference of the run that measured the commit)", task)
	}
	if !CommitOK(commit) {
		return "", fmt.Errorf("attest %s: commit %q is not 7 to 40 hex characters", task, commit)
	}
	if session == "" {
		return "", &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("an attestation for %s requires the lead's --session", task)}
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
		return "", &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("session %q is a worker session for %s; an attestation must come from the lead", session, task)}
	}
	_, attempt, _ := latestBaseBriefAndAttempt(events, task)
	if attempt == "" {
		return "", &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s has no dispatched attempt; only a dispatched unit's readings can be attested", task)}
	}
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return "", fmt.Errorf("attest %s: %w", task, err)
	}
	tree := landedTree(dir, commit)
	if tree == "" {
		return "", fmt.Errorf("attest %s: commit %s is not in the repository at %s", task, commit, dir)
	}
	out, err := gitRead(dir, []string{"diff", "--name-only", commit + "^", commit})
	if err != nil {
		return "", fmt.Errorf("attest %s: list the paths commit %s changed: %w", task, commit, err)
	}
	var outside []string
	for _, line := range strings.Split(out, "\n") {
		p := strings.TrimSpace(line)
		if p == "" || p == "flywheel.md" || strings.HasPrefix(p, ".flywheel/") {
			continue
		}
		if !ownsContains(header.Owns, p) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		return "", &RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("commit %s changed paths outside %s's owns (%s); an attestation vouches for the unit's own change only", commit, task, strings.Join(outside, ", "))}
	}

	reading := func(gate, command string) Event {
		return Event{
			Task: task, Kind: "validated", Attempt: attempt, Gate: gate, Command: command,
			Tree: tree, Commit: commit, RC: new(int), Session: session,
			Source: "external", Evidence: evidence, Persona: "supervisor",
		}
	}
	var batch []Event
	for i, g := range header.Gates {
		batch = append(batch, reading(fmt.Sprintf("%d", i+1), g))
	}
	for i, g := range header.LiveGates {
		batch = append(batch, reading(fmt.Sprintf("live%d", i+1), g))
	}
	batch = append(batch, Event{
		Task: task, Kind: "owns_checked", Attempt: attempt, Tree: tree, Commit: commit,
		Session: session, Source: "external", Evidence: evidence, Persona: "supervisor",
	})
	if err := AppendEvents(dir, batch); err != nil {
		return "", fmt.Errorf("append attestation for %s: %w", task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return "", fmt.Errorf("refresh state for %s: %w", task, err)
	}
	return tree, nil
}
