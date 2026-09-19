package flywheel

import "fmt"

// T7Refusal returns the T7 refusal for landing task, or nil: nil when the
// task's line is "" (unknown), cleared and not stopped. Pure: events only.
func T7Refusal(events []Event, task string) *RuleRefusal {
	// One chronological scan. A unit's line is the adapter/model of its
	// latest dispatch before its latest inspected pass; an audited event
	// credits the line behind the task's pass at that point in the log, and
	// a line's state follows its latest credited audit: a conforming audit
	// clears it (and closes an earlier nonconformance), a nonconformance
	// stops it until a later conforming audit.
	lastDispatch := map[string]string{} // task -> line of its latest dispatch so far
	passLine := map[string]string{}     // task -> line behind its latest pass so far
	type lineState struct {
		cleared   bool
		stoppedBy string // the task whose nonconformance is the line's latest audit
	}
	lines := map[string]*lineState{}
	for _, e := range events {
		switch {
		case e.Kind == "dispatched":
			l := ""
			if e.Adapter != "" && e.Model != "" {
				l = e.Adapter + "/" + e.Model
			}
			lastDispatch[e.Task] = l
		case e.Kind == "inspected" && e.Verdict == "pass":
			passLine[e.Task] = lastDispatch[e.Task]
		case e.Kind == "audited":
			l := passLine[e.Task]
			if l == "" {
				continue
			}
			st := lines[l]
			if st == nil {
				st = &lineState{}
				lines[l] = st
			}
			switch e.Verdict {
			case "conforms":
				st.cleared = true
				st.stoppedBy = ""
			case "nonconformance":
				st.stoppedBy = e.Task
			}
		}
	}

	line := passLine[task]
	if line == "" {
		return nil
	}
	st := lines[line]
	if st != nil && st.stoppedBy != "" {
		return &RuleRefusal{
			Rule: "T7",
			Fix:  fmt.Sprintf("line %s has an open nonconformance (the latest audit, of %s); fix it and re-audit a unit of that line (flywheel audit <task> --session <auditor>) before more of its units land, or land with --exception <evidence> --session <lead>", line, st.stoppedBy),
		}
	}
	if st == nil || !st.cleared {
		return &RuleRefusal{
			Rule: "T7",
			Fix:  fmt.Sprintf("line %s has no conforming first-article audit yet; audit its first article from an independent session (flywheel audit --first-article --session <auditor>) before its units land, or land with --exception <evidence> --session <lead>", line),
		}
	}
	return nil
}
