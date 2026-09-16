package flywheel

import (
	"errors"
	"fmt"
	"time"
)

// InspectOptions configures one inspection.
type InspectOptions struct {
	Dir     string // flywheel root; default "."
	Workdir string // git working tree to hash; default Dir
	Verdict string
	Session string
	Note    string
}

// RuleRefusal is a poka-yoke refusal: the transition rule that fired and the
// fix that clears it. The CLI exits 6 on any RuleRefusal and 1 on other
// errors, so it is distinguished with errors.As.
type RuleRefusal struct {
	Rule string
	Fix  string
}

func (r *RuleRefusal) Error() string {
	return r.Rule + ": " + r.Fix
}

// InspectTask records an inspected event for a task after enforcing the
// transition rules T3, T4 and T8. On success the inspected event is appended
// and the derived state refreshed.
func InspectTask(dir, task string, o InspectOptions) error {
	if o.Dir == "" {
		o.Dir = "."
	}
	// T8: refuse a verdict outside the set, and record persona inspector.
	switch o.Verdict {
	case "pass", "rework", "scrap", "escalate":
	default:
		return &RuleRefusal{Rule: "T8", Fix: fmt.Sprintf("verdict %q is not pass, rework, scrap, or escalate", o.Verdict)}
	}
	if o.Session == "" {
		return &RuleRefusal{Rule: "T4", Fix: "an inspector --session is required"}
	}
	// T4: the inspector's session must differ from every worker session. This
	// runs before the T3 readings check, so a worker-session inspection is
	// refused as T4 even when its readings are also missing.
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return err
	}
	if r := sessionClash(task, events, o.Session); r != "" {
		return &RuleRefusal{Rule: "T4", Fix: r}
	}
	var e RuleRefusal
	if o.Verdict == "pass" {
		e, err = requireReadings(o.Dir, wd(o.Workdir, o.Dir), task, events)
		if err != nil {
			return err
		}
		if e.Rule != "" {
			return &e
		}
	}
	tree, err := treeHash(wd(o.Workdir, o.Dir))
	if err != nil {
		return err
	}
	if err := AppendEvent(o.Dir, Event{
		TS: "", Task: task, Kind: "inspected", Verdict: o.Verdict,
		Tree: tree, Session: o.Session, Note: o.Note, Persona: "inspector",
	}); err != nil {
		return err
	}
	_, _ = WriteState(o.Dir)
	return nil
}

// wd returns the working tree to hash, defaulting to dir.
func wd(workdir, dir string) string {
	if workdir == "" {
		return dir
	}
	return workdir
}

// requireReadings enforces T3 for a pass verdict: the tree computed in wd must
// have a passing validated reading for every gate in the brief header, all
// recorded after the task's latest finished event, and a clean owns_checked
// for that tree. A non-empty rule on the returned value is a refusal.
func requireReadings(dir, wd, task string, events []Event) (RuleRefusal, error) {
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		if errors.Is(err, errNoPlannedBrief) {
			return RuleRefusal{Rule: "T3", Fix: "task has no planned brief"}, nil
		}
		return RuleRefusal{}, err
	}
	if len(header.Gates) == 0 {
		return RuleRefusal{Rule: "T3", Fix: "brief declares no gate: lines; add gate: lines to the brief header"}, nil
	}
	tree, err := treeHash(wd)
	if err != nil {
		return RuleRefusal{}, err
	}
	latest := latestFinished(events, task)
	for i := range header.Gates {
		idx := fmt.Sprintf("%d", i+1)
		if !hasPassingValidated(events, task, idx, tree, latest) {
			return RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("no passing supervisor validated reading for gate %s on tree %s after the latest finished event; run: flywheel validate %s", idx, tree, task)}, nil
		}
	}
	if !hasCleanOwnsChecked(events, task, tree, latest) {
		return RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("no clean owns_checked for tree %s after the latest finished event; run: flywheel validate %s", tree, task)}, nil
	}
	return RuleRefusal{}, nil
}

// latestFinished returns the latest finished event's timestamp for task, or
// the zero time when there is none.
func latestFinished(events []Event, task string) time.Time {
	var latest time.Time
	for _, e := range events {
		if e.Task != task || e.Kind != "finished" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// hasPassingValidated reports whether task has, after after, a validated event
// for gate idx on tree whose reading is passing (rc 0 and not host-blocked).
func hasPassingValidated(events []Event, task, idx, tree string, after time.Time) bool {
	for _, e := range events {
		if e.Task != task || e.Kind != "validated" || e.Gate != idx || e.Tree != tree {
			continue
		}
		if e.Reason == "host-blocked" {
			continue
		}
		if e.RC == nil || *e.RC != 0 {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(after) {
			return true
		}
	}
	return false
}

// hasCleanOwnsChecked reports whether task has, after after, an owns_checked
// for tree with nothing outside.
func hasCleanOwnsChecked(events []Event, task, tree string, after time.Time) bool {
	for _, e := range events {
		if e.Task != task || e.Kind != "owns_checked" || e.Tree != tree {
			continue
		}
		if len(e.Outside) != 0 {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(after) {
			return true
		}
	}
	return false
}

// sessionClash reports which worker session collides with sess, or "" when
// sess is not a worker session of the task. The worker-event set matches
// verify.go's ruleT4, so an inspector's own inspected events never count:
// only started, finished, dispatched, report and worker_plan events carry
// worker sessions.
func sessionClash(task string, events []Event, sess string) string {
	for _, e := range events {
		if e.Task == task && e.Session == sess {
			switch e.Kind {
			case "started", "finished", "dispatched", "report", "worker_plan":
				return fmt.Sprintf("session %q is a worker session of task %q; use a distinct inspector --session", sess, task)
			}
		}
	}
	return ""
}

// IsRuleRefusal reports whether err is a poka-yoke rule refusal.
func IsRuleRefusal(err error) bool {
	var r *RuleRefusal
	return errors.As(err, &r)
}
