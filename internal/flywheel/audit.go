package flywheel

import (
	"fmt"
	"os"
	"strings"
)

// AuditOptions configures one audit (issue #61).
type AuditOptions struct {
	Dir     string // flywheel root; default "."
	Workdir string // git working tree whose current tree is audited; default Dir
	Session string // the auditor's session: required, and independent of the unit
	Note    string
	// RequireCandidate (selection modes) refuses the audit when the unit is
	// no longer an audit candidate — audited since it was selected, or its
	// latest inspection no longer a pass — checked before the gates run and
	// again under the lock the audit records under (#323 review). A single
	// named task leaves it off, so a re-audit that clears a nonconformance
	// stays possible.
	RequireCandidate bool
}

// notCandidate returns the refusal RequireCandidate reports, or nil when task
// is still an audit candidate in events.
func notCandidate(events []Event, task string) *RuleRefusal {
	for _, c := range AuditCandidates(events) {
		if c == task {
			return nil
		}
	}
	return &RuleRefusal{Rule: "audit", Fix: fmt.Sprintf("task %s is no longer an audit candidate (audited since it was selected, or its latest inspection is not a pass); select again", task)}
}

// AuditResult is what one audit found.
type AuditResult struct {
	Verdict  string   `json:"verdict"` // "conforms" or "nonconformance"
	Tree     string   `json:"tree"`
	Findings []string `json:"findings"` // empty when the unit conforms
}

// AuditTask re-measures a unit in a clean copy of its tree and checks its
// record against the verify rules, from an independent session. The auditor's
// session must not have planned, built or inspected the unit. On success the
// audited event is recorded with conforms or nonconformance and the findings.
func AuditTask(dir, task string, o AuditOptions) (AuditResult, error) {
	if o.Dir == "" {
		o.Dir = "."
	}

	// T4: session is required.
	if o.Session == "" {
		return AuditResult{}, &RuleRefusal{Rule: "T4", Fix: "an auditor --session is required"}
	}

	events, err := ReadEvents(o.Dir)
	if err != nil {
		return AuditResult{}, err
	}

	// An audit measures the product where the auditor points it — by default
	// the flywheel root, not the unit's own (possibly removed) worktree.
	workdir := wd(o.Workdir, o.Dir, nil, task)

	if r := auditIndependence(events, task, o.Session); r != nil {
		return AuditResult{}, r
	}
	if o.RequireCandidate {
		if r := notCandidate(events, task); r != nil {
			return AuditResult{}, r
		}
	}

	// Task must have events.
	hasEvents := false
	for _, e := range events {
		if e.Task == task {
			hasEvents = true
			break
		}
	}
	if !hasEvents {
		return AuditResult{}, fmt.Errorf("no events for task %s", task)
	}

	// Get the header (brief header).
	header, _, err := AttemptBrief(o.Dir, events, task)
	if err != nil {
		return AuditResult{}, err
	}

	var findings []string

	// The tree this audit measures, captured before the clean copy is made;
	// re-checked before recording, so the verdict never names a tree it did
	// not measure (#301 review).
	tree, err := treeHash(workdir)
	if err != nil {
		return AuditResult{}, err
	}

	// Re-measure gates in a clean copy.
	tmp, err := isolateWorktree(workdir)
	if err != nil {
		return AuditResult{}, err
	}
	defer os.RemoveAll(tmp)

	for i, gate := range header.Gates {
		rc, _, _, gerr := runGate(tmp, gate)
		if gerr != nil {
			return AuditResult{}, gerr
		}
		if rc != 0 {
			findings = append(findings, fmt.Sprintf("gate %d failed in a clean copy (rc=%d): %s", i+1, rc, gate))
		}
	}

	// Check the record with VerifyTasks.
	verifyRes, err := VerifyTasks(o.Dir, VerifyOptions{Dir: o.Dir, Tasks: []string{task}, Workdir: o.Workdir})
	if err != nil {
		return AuditResult{}, err
	}
	for _, item := range verifyRes.Items {
		switch {
		case item.Inconclusive:
			// An audit that cannot establish a check does not pass it.
			findings = append(findings, fmt.Sprintf("record %s could not be established: %s", item.Rule, item.Reason))
		case !item.Pass:
			findings = append(findings, fmt.Sprintf("record %s: %s", item.Rule, item.Reason))
		}
	}

	if after, err := treeHash(workdir); err != nil {
		return AuditResult{}, err
	} else if after != tree {
		return AuditResult{}, fmt.Errorf("the tree changed during the audit (%s -> %s); nothing was recorded, rerun it on a quiet tree", short(tree), short(after))
	}

	if findings == nil {
		findings = []string{} // JSON prints [] for a clean audit, never null
	}

	// Determine verdict.
	verdict := "conforms"
	if len(findings) > 0 {
		verdict = "nonconformance"
	}

	// Build the note.
	note := o.Note
	if len(findings) > 0 {
		findingsStr := strings.Join(findings, "; ")
		if note != "" {
			note = note + "; " + findingsStr
		} else {
			note = findingsStr
		}
	}

	// Record under the dispatch lock — the lock flywheel land holds while it
	// decides T7 and appends — so an audit can never land between a
	// landing's read of the line's audits and its landed event (#317
	// review). Lock order: dispatch.lock, then events.lock inside AppendEvent.
	release, err := acquireRepoLock(o.Dir, "dispatch.lock", defaultRepoLockTimings())
	if err != nil {
		return AuditResult{}, err
	}
	defer release()

	// Independence again, right before recording: the session may have
	// acted on the unit while the gates ran (#301 review).
	if fresh, err := ReadEvents(o.Dir); err != nil {
		return AuditResult{}, err
	} else if r := auditIndependence(fresh, task, o.Session); r != nil {
		return AuditResult{}, r
	} else if o.RequireCandidate {
		if r := notCandidate(fresh, task); r != nil {
			return AuditResult{}, r
		}
	}

	// Append the audited event.
	if err := AppendEvent(o.Dir, Event{
		Task: task, Kind: "audited", Verdict: verdict, Session: o.Session, Tree: tree, Note: note, Persona: "auditor",
		Workdir: workdirField(workdir, o.Dir),
	}); err != nil {
		return AuditResult{}, err
	}

	// Refresh derived state; the audit is recorded either way, but a stale
	// state file must not be reported as success (#301 review).
	if _, err := WriteState(o.Dir); err != nil {
		return AuditResult{}, fmt.Errorf("audit recorded, but refreshing state failed: %w", err)
	}

	return AuditResult{
		Verdict:  verdict,
		Tree:     tree,
		Findings: findings,
	}, nil
}

// auditIndependence refuses (T4) an audit from a session that planned,
// amended, built, inspected or reviewed the task: an audit is only worth
// something from a session that neither made nor judged the unit.
func auditIndependence(events []Event, task, session string) *RuleRefusal {
	for _, e := range events {
		if e.Task != task || e.Session != session {
			continue
		}
		switch e.Kind {
		case "planned", "amended", "dispatched", "started", "worker_plan", "report", "finished", "inspected", "reviewed":
			return &RuleRefusal{
				Rule: "T4",
				Fix:  fmt.Sprintf("session %q planned, built or inspected task %s; an audit must come from an independent session", session, task),
			}
		}
	}
	return nil
}
