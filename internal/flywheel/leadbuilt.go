package flywheel

import (
	"fmt"
	"strconv"
	"strings"
)

// leadBuilt reports whether task is lead-built (issue #722): it has no
// dispatched event after its latest planned event. An attempt dispatched
// before a re-plan does not count.
func leadBuilt(events []Event, task string) bool {
	dispatched := false
	for _, e := range events {
		if e.Task != task {
			continue
		}
		switch e.Kind {
		case "planned":
			dispatched = false
		case "dispatched":
			dispatched = true
		}
	}
	return !dispatched
}

// leadBuiltMark is the mark factory and recover show for a lead-built unit
// (issue #722): "built by lead, <n> changed lines" (plus ", exception: <why>")
// from the latest inspected event after the latest planned one when it has
// LeadBuilt, or "built by lead" for a lead-built unit validated but not yet
// inspected. A unit a worker built never gets a mark.
func leadBuiltMark(events []Event, task string) (string, bool) {
	var inspected *Event
	validated := false
	for i, e := range events {
		if e.Task != task {
			continue
		}
		switch e.Kind {
		case "planned":
			inspected, validated = nil, false
		case "validated":
			validated = true
		case "inspected":
			inspected = &events[i]
		}
	}
	if inspected != nil {
		if !inspected.LeadBuilt || !leadBuilt(events, task) {
			return "", false
		}
		mark := fmt.Sprintf("built by lead, %d changed lines", inspected.ChangedLines)
		if inspected.Exception != "" {
			mark += ", exception: " + inspected.Exception
		}
		return mark, true
	}
	if validated && leadBuilt(events, task) {
		return "built by lead", true
	}
	return "", false
}

// leadBuiltBase is the commit a lead-built unit's changed lines are counted
// from: UnitBase when the unit has one, else the latest planned event's Base
// (HEAD when it was planned), else "".
func leadBuiltBase(events []Event, task string) string {
	if b := UnitBase(events, task); b != "" {
		return b
	}
	base := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "planned" {
			base = e.Base
		}
	}
	return base
}

// reviewOwed reports the agent review a lead-built exception unit owes
// (issue #789): its latest passing inspected event has LeadBuilt and an
// Exception, and no agent review (agentReviewed) of the task follows it,
// whatever that review's verdict. The string is the command that pays it.
func reviewOwed(events []Event, task string) (string, bool) {
	pass, reviewed := -1, false
	for i, e := range events {
		if e.Task != task {
			continue
		}
		switch {
		case e.Kind == "inspected" && e.Verdict == "pass":
			pass, reviewed = i, false
		case pass >= 0 && agentReviewed(e):
			reviewed = true
		}
	}
	if pass < 0 || reviewed || !events[pass].LeadBuilt || events[pass].Exception == "" {
		return "", false
	}
	base := leadBuiltBase(events, task)
	if base == "" {
		base = "<the commit before the unit>"
	}
	return fmt.Sprintf("flywheel review %s --agent --base %s --session <reviewer>", task, base), true
}

// leadBuiltRefusal refuses a pass (rule lead-built) of a lead-built unit
// whose lines exceed max, unless an exception records why.
func leadBuiltRefusal(lines, max int, exception, task string) *RuleRefusal {
	if lines <= max || exception != "" {
		return nil
	}
	return &RuleRefusal{Rule: "lead-built", Fix: fmt.Sprintf("task %s has no dispatched attempt and changes %d lines, over lead_built.max_changed_lines %d; dispatch a worker (flywheel run %s) or record why with --exception \"<why>\"",
		task, lines, max, task)}
}

// commitChangedLines counts added plus deleted lines of git diff --numstat
// from base to commit (base "" means commit's parent). A binary file ("-")
// counts 0. Flywheel's own files (isFlywheelOwnPath) are not counted (issue
// #729); --no-renames keeps the path field a plain path.
func commitChangedLines(wd, base, commit string) (int, error) {
	if base == "" {
		base = commit + "^"
	}
	stat, err := gitRead(wd, []string{"diff", "--numstat", "--no-renames", base, commit})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(stat, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 || isFlywheelOwnPath(strings.TrimSpace(f[2])) {
			continue
		}
		added, _ := strconv.Atoi(f[0])
		deleted, _ := strconv.Atoi(f[1])
		n += added + deleted
	}
	return n, nil
}
