package flywheel

import (
	"fmt"
	"slices"
	"strings"
)

// redFirstLintProblem is flywheel lint --probe's problem for a kind: fix brief
// whose gates all passed on the base tree (issue #648).
const redFirstLintProblem = "kind: fix needs a gate that fails before the fix (a regression test); " +
	"every gate passed on the base tree (rule red-first; lint.red_first false turns it off)"

// probeRed reports whether a gate probe counts as red on the base tree: the
// gate started and failed (issue #648). A gate that cannot start (a spawn
// error, exit 126 or 127) is not a regression test.
func probeRed(rc int, err error) bool {
	return err == nil && rc != 0 && rc != 126 && rc != 127
}

// RedFirstLintProblem returns lint's red-first problem for a brief of kind
// whose gates were probed on the base tree, or "" (issue #648): only a
// kind: fix brief with the rule on, at least one probe and no red probe has
// the problem.
func RedFirstLintProblem(kind string, on bool, probes []GateProbe) string {
	if kind != "fix" || !on || len(probes) == 0 {
		return ""
	}
	for _, p := range probes {
		if probeRed(p.RC, p.Err) {
			return ""
		}
	}
	return redFirstLintProblem
}

// redFirstRefusal refuses a pass (rule red-first) of a kind: fix task with the
// rule on unless one of its gates failed on the base tree before dispatch
// (issue #648). Only the task's gate_probed events before its first
// dispatched event count, the newest per command, and a red probe counts only
// when its command is still one of header's gates.
func redFirstRefusal(events []Event, task string, header BriefHeader, on bool) *RuleRefusal {
	if header.Kind != "fix" || !on {
		return nil
	}
	newest := preDispatchProbes(events, task)
	for cmd, e := range newest {
		if e.RC != nil && probeRed(*e.RC, nil) && slices.Contains(header.Gates, cmd) {
			return nil
		}
	}
	record := fmt.Sprintf("add a gate that runs the regression test, and record the base probe with "+
		"`flywheel lint <brief> --probe --task %s` before `flywheel run`; or set lint.red_first false", task)
	if len(newest) == 0 {
		return &RuleRefusal{Rule: "red-first", Fix: fmt.Sprintf("kind: fix task %s has no gate probe recorded before dispatch, "+
			"so no gate failed on the base tree before dispatch; %s", task, record)}
	}
	return &RuleRefusal{Rule: "red-first", Fix: fmt.Sprintf("kind: fix task %s: no gate failed on the base tree before dispatch "+
		"(every pre-dispatch probe on a gate still in the brief passed); %s", task, record)}
}

// preDispatchProbes returns task's gate_probed events before its first
// dispatched event, the newest per command (issue #648).
func preDispatchProbes(events []Event, task string) map[string]Event {
	newest := map[string]Event{}
	for _, e := range derivationOrder(events) {
		if e.Task != task {
			continue
		}
		if e.Kind == "dispatched" {
			break
		}
		if e.Kind == "gate_probed" && e.Command != "" {
			newest[e.Command] = e
		}
	}
	return newest
}

// redFirstDeltaRefusal refuses a correction (rule red-first, issue #825) of a
// kind: fix task with the rule on whose merged header after drops every gate
// that failed on the base tree before the first dispatch while the header
// before (the attempt being replaced) still keeps one: inspect would refuse
// the pass and no later probe counts. A before that already fails red-first
// is never refused: the correction cannot make it worse.
func redFirstDeltaRefusal(events []Event, task string, before, after BriefHeader, on bool) *RuleRefusal {
	if redFirstRefusal(events, task, before, on) != nil || redFirstRefusal(events, task, after, on) == nil {
		return nil
	}
	var red []string
	for cmd, e := range preDispatchProbes(events, task) {
		if e.RC != nil && probeRed(*e.RC, nil) && slices.Contains(before.Gates, cmd) {
			red = append(red, cmd)
		}
	}
	slices.Sort(red)
	quoted := make([]string, len(red))
	for i, c := range red {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	return &RuleRefusal{Rule: "red-first", Fix: fmt.Sprintf("kind: fix task %s: the correction's gates drop the gate that failed on the base tree "+
		"before dispatch (%s), so inspect could never pass it; keep that gate: line verbatim in the delta and add any new gate as another line; "+
		"or set lint.red_first false", task, strings.Join(quoted, ", "))}
}
