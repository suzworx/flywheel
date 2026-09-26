package flywheel

import (
	"fmt"
	"io"
	"strings"
)

// ExplainEntry is one line of a task's timeline: when, which kind, the
// attempt when the event carries one, and a one-line human summary.
type ExplainEntry struct {
	TS      string `json:"ts"`
	Kind    string `json:"kind"`
	Attempt string `json:"attempt,omitempty"`
	Line    string `json:"line"`
}

// Explanation is a task's whole story folded from the event log.
type Explanation struct {
	Task         string         `json:"task"`
	Status       string         `json:"status"`
	Brief        string         `json:"brief,omitempty"`
	BriefSHA256  string         `json:"brief_sha256,omitempty"`
	Owns         []string       `json:"owns,omitempty"`
	Needs        []string       `json:"needs,omitempty"`
	Gates        []string       `json:"gates,omitempty"`
	GoalID       string         `json:"goal_id,omitempty"`
	Planner      string         `json:"planner,omitempty"`
	PlannerModel string         `json:"planner_model,omitempty"`
	Attempts     []string       `json:"attempts,omitempty"`
	Steps        int            `json:"steps"`
	Cost         float64        `json:"cost"`
	Commit       string         `json:"commit,omitempty"`
	Tree         string         `json:"tree,omitempty"`
	Timeline     []ExplainEntry `json:"timeline"`
}

// explainLine formats one event as a one-line human summary.
func explainLine(e Event) string {
	switch e.Kind {
	case "planned":
		line := fmt.Sprintf("planned brief %s", e.Brief)
		if e.Session != "" {
			line += fmt.Sprintf(" by %s", e.Session)
		}
		if e.Model != "" {
			line += fmt.Sprintf(" (%s)", e.Model)
		}
		if e.GoalID != "" {
			line += fmt.Sprintf(" for goal %s", e.GoalID)
		}
		return line

	case "amended":
		line := fmt.Sprintf("amended brief %s", e.Brief)
		if e.Note != "" {
			line += fmt.Sprintf(": %s", e.Note)
		}
		return line

	case "dispatched":
		if r := e.Route; r != nil {
			line := words("dispatched", e.Attempt, "to", e.Adapter, e.Model, "routed", r.Pick, "by", r.Objective)
			if r.Basis == "kind" {
				line += " on " + r.Kind
			}
			return line
		}
		return words("dispatched", e.Attempt, "to", e.Adapter, e.Model)

	case "started":
		return words("started", e.Attempt, "session", e.Session)

	case "worker_plan":
		return fmt.Sprintf("worker plan recorded (%s)", e.Path)

	case "no-plan", "off-course":
		line := fmt.Sprintf("%s on %s", e.Kind, e.Attempt)
		if e.Note != "" {
			line += fmt.Sprintf(": %s", e.Note)
		}
		return line

	case "finished":
		var rc string
		if e.RC != nil {
			rc = fmt.Sprintf("%d", *e.RC)
		} else {
			rc = "?"
		}
		line := fmt.Sprintf("finished %s rc=%s reason=%s steps=%d cost=$%.4f", e.Attempt, rc, e.Reason, e.Steps, e.Cost)
		if len(e.Wrote) > 0 {
			line += fmt.Sprintf(" wrote %d files", len(e.Wrote))
		}
		return line

	case "report":
		path := ""
		if e.Path != "" {
			path = "(" + e.Path + ")"
		}
		return words("report", e.Attempt, path)

	case "validated":
		var line string
		if e.RC != nil && *e.RC == 0 {
			line = fmt.Sprintf("gate %s: pass", e.Gate)
		} else if e.Reason == "inconclusive" {
			line = fmt.Sprintf("gate %s: inconclusive", e.Gate)
		} else {
			var rc string
			if e.RC != nil {
				rc = fmt.Sprintf("%d", *e.RC)
			} else {
				rc = "?"
			}
			line = fmt.Sprintf("gate %s: fail rc=%s", e.Gate, rc)
		}
		if e.DurationMS > 0 {
			line += fmt.Sprintf(" in %dms", e.DurationMS)
		}
		if e.Tree != "" {
			line += fmt.Sprintf(" tree %s", hash8(e.Tree))
		}
		if e.Path != "" {
			line += fmt.Sprintf(" (%s)", e.Path)
		}
		return line

	case "owns_checked":
		if len(e.Outside) == 0 {
			return "owns check: ok"
		}
		return fmt.Sprintf("owns check: outside %s", strings.Join(e.Outside, ", "))

	case "inspected":
		line := fmt.Sprintf("inspected %s by %s", e.Verdict, e.Session)
		if e.Persona != "" {
			line += fmt.Sprintf(" (%s)", e.Persona)
		}
		return line

	case "reviewed":
		line := fmt.Sprintf("reviewed %s by %s", e.Verdict, e.Session)
		if e.Model != "" {
			line += fmt.Sprintf(" (%s)", e.Model)
		}
		return line
	case "review_finding":
		return fmt.Sprintf("%s finding %s at %s:%d: %s", e.Severity, e.Finding, e.Path, e.LineNo, e.Title)
	case "finding_response":
		line := fmt.Sprintf("finding %s %s", e.Finding, e.Verdict)
		if e.Session != "" {
			line += " by " + e.Session
		}
		if e.Note != "" {
			line += ": " + e.Note
		}
		return line

	case "signal":
		return words("signal", e.Signal, "on", e.Attempt)

	case "learning":
		return fmt.Sprintf("learning %s %s", e.Severity, e.Title)

	case "note":
		// A journal line (issue #409): the text is the whole event.
		return "note: " + e.Note

	case "blocked", "lost":
		line := fmt.Sprintf("%s %s: %s", e.Kind, e.Attempt, e.Reason)
		if e.Note != "" {
			line += fmt.Sprintf(" %s", e.Note)
		}
		return line

	case "landed":
		line := fmt.Sprintf("landed %s", e.Commit)
		if e.Tree != "" {
			line += fmt.Sprintf(" tree %s", hash8(e.Tree))
		}
		if e.Note != "" {
			line += fmt.Sprintf(" — %s", e.Note)
		}
		return line

	default:
		line := e.Kind
		if e.Attempt != "" {
			line += fmt.Sprintf(" %s", e.Attempt)
		}
		if e.Note != "" {
			line += fmt.Sprintf(": %s", e.Note)
		}
		return line
	}
}

// Explain folds a task's events from the ledger into one Explanation.
// Returns an error if there are no events for the task.
func Explain(events []Event, task string) (Explanation, error) {
	var taskEvents []Event
	for _, e := range derivationOrder(events) {
		if e.Task == task {
			taskEvents = append(taskEvents, e)
		}
	}

	if len(taskEvents) == 0 {
		return Explanation{}, fmt.Errorf("no events for task %s", task)
	}

	s := Derive(events)
	explanation := Explanation{
		Task:     task,
		Timeline: []ExplainEntry{},
	}

	// Get status from derived state
	for _, ts := range s.Tasks {
		if ts.ID == task {
			explanation.Status = ts.Status
			break
		}
	}

	// Get info from planned event (first one) and amended event (latest one)
	var latestPlanOrAmend *Event
	firstPlanned := true
	for i, e := range taskEvents {
		if e.Kind == "planned" || e.Kind == "amended" {
			latestPlanOrAmend = &taskEvents[i]
			if e.Kind == "planned" && firstPlanned {
				explanation.GoalID = e.GoalID
				explanation.Planner = e.Session
				explanation.PlannerModel = e.Model
				firstPlanned = false
			}
		}
	}

	if latestPlanOrAmend != nil {
		explanation.Brief = latestPlanOrAmend.Brief
		explanation.Owns = latestPlanOrAmend.Owns
		explanation.Needs = NeedTargets(latestPlanOrAmend.Needs...) // a legacy ["none"] is no dependency (#310 review)
		if latestPlanOrAmend.Header != nil {
			explanation.Gates = latestPlanOrAmend.Header.Gates
			explanation.BriefSHA256 = latestPlanOrAmend.Header.SHA256
		}
	}

	// Collect attempts in order
	seen := map[string]bool{}
	for _, e := range taskEvents {
		if e.Kind == "dispatched" && e.Attempt != "" && !seen[e.Attempt] {
			seen[e.Attempt] = true
			explanation.Attempts = append(explanation.Attempts, e.Attempt)
		}
	}

	// Sum steps and cost from finished events
	for _, e := range taskEvents {
		if e.Kind == "finished" {
			explanation.Steps += e.Steps
			explanation.Cost += e.Cost
		}
	}

	// Get commit and tree from last landed event
	for i := len(taskEvents) - 1; i >= 0; i-- {
		if taskEvents[i].Kind == "landed" {
			explanation.Commit = taskEvents[i].Commit
			explanation.Tree = taskEvents[i].Tree
			break
		}
	}

	// Build timeline
	for _, e := range taskEvents {
		entry := ExplainEntry{
			TS:      e.TS,
			Kind:    e.Kind,
			Attempt: e.Attempt,
			Line:    explainLine(e),
		}
		explanation.Timeline = append(explanation.Timeline, entry)
	}

	return explanation, nil
}

// RenderExplanation writes x as Markdown to w.
func RenderExplanation(w io.Writer, x Explanation) error {
	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "# %s — %s\n\n", x.Task, x.Status)

	// Summary section
	if x.Brief != "" {
		fmt.Fprintf(&b, "- brief: %s", x.Brief)
		if x.BriefSHA256 != "" {
			fmt.Fprintf(&b, " (sha256 %s)", hash8(x.BriefSHA256))
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(x.Owns) > 0 {
		fmt.Fprintf(&b, "- owns: %s\n", strings.Join(x.Owns, ", "))
	}

	if len(x.Needs) > 0 {
		fmt.Fprintf(&b, "- needs: %s\n", strings.Join(x.Needs, ", "))
	}

	if len(x.Gates) > 0 {
		fmt.Fprintf(&b, "- gates: %d\n", len(x.Gates))
	}

	if x.Planner != "" {
		fmt.Fprintf(&b, "- planner: %s", x.Planner)
		if x.PlannerModel != "" {
			fmt.Fprintf(&b, " (%s)", x.PlannerModel)
		}
		fmt.Fprintf(&b, "\n")
	}

	if x.GoalID != "" {
		fmt.Fprintf(&b, "- goal: %s\n", x.GoalID)
	}

	if len(x.Attempts) > 0 {
		fmt.Fprintf(&b, "- attempts: %s\n", strings.Join(x.Attempts, ", "))
	}

	fmt.Fprintf(&b, "- steps: %d, cost: $%.4f\n", x.Steps, x.Cost)

	if x.Commit != "" {
		fmt.Fprintf(&b, "- landed: %s", x.Commit)
		if x.Tree != "" {
			fmt.Fprintf(&b, " (tree %s)", hash8(x.Tree))
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "\n## Timeline\n\n")

	// Timeline entries
	for _, e := range x.Timeline {
		fmt.Fprintf(&b, "- `%s` **%s** %s\n", e.TS, e.Kind, e.Line)
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write explanation: %w", err)
	}
	return nil
}

// hash8 returns the first 8 characters of a hash for display, or the whole
// string when it is shorter (verify.go's short cuts to 12 for its own report).
func hash8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// words joins the non-empty parts with single spaces, so a line never shows a
// double space where an event lacks a field (a started event carries no
// attempt, for instance).
func words(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
