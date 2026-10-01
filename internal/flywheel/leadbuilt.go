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
// counts 0.
func commitChangedLines(wd, base, commit string) (int, error) {
	if base == "" {
		base = commit + "^"
	}
	stat, err := gitRead(wd, []string{"diff", "--numstat", base, commit})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(stat, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		added, _ := strconv.Atoi(f[0])
		deleted, _ := strconv.Atoi(f[1])
		n += added + deleted
	}
	return n, nil
}
