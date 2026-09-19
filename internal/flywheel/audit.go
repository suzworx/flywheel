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
	workdir := wd(o.Workdir, o.Dir)

	// T4: session is required.
	if o.Session == "" {
		return AuditResult{}, &RuleRefusal{Rule: "T4", Fix: "an auditor --session is required"}
	}

	events, err := ReadEvents(o.Dir)
	if err != nil {
		return AuditResult{}, err
	}

	// T4: independence check. The auditor's session must not have planned,
	// amended, dispatched, started, worker_plan, report, finished, inspected
	// or reviewed the task.
	for _, e := range events {
		if e.Task != task || e.Session != o.Session {
			continue
		}
		switch e.Kind {
		case "planned", "amended", "dispatched", "started", "worker_plan", "report", "finished", "inspected", "reviewed":
			return AuditResult{}, &RuleRefusal{
				Rule: "T4",
				Fix:  fmt.Sprintf("session %q planned, built or inspected task %s; an audit must come from an independent session", o.Session, task),
			}
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
		if !item.Pass && !item.Inconclusive {
			findings = append(findings, fmt.Sprintf("record %s: %s", item.Rule, item.Reason))
		}
	}

	// Get tree hash.
	tree, err := treeHash(workdir)
	if err != nil {
		return AuditResult{}, err
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

	// Append the audited event.
	if err := AppendEvent(o.Dir, Event{
		Task: task, Kind: "audited", Verdict: verdict, Session: o.Session, Tree: tree, Note: note, Persona: "auditor",
		Workdir: workdirField(workdir, o.Dir),
	}); err != nil {
		return AuditResult{}, err
	}

	// Refresh derived state.
	_, _ = WriteState(o.Dir)

	return AuditResult{
		Verdict:  verdict,
		Tree:     tree,
		Findings: findings,
	}, nil
}
