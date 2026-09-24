package flywheel

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// findingKey is what identifies a defect across review rounds (issue #389):
// the file and the claim text, case- and space-insensitive. The reviewer
// re-reports a still-present defect under a new id, so rounds match on this,
// never on the id.
func findingKey(e Event) string {
	return strings.ToLower(e.Path) + "\x00" + strings.Join(strings.Fields(strings.ToLower(e.Title)), " ")
}

// agentReviewed reports whether e is a reviewed event written by the review
// agent (persona reviewer, an adapter recorded), not a verdict passed in by
// hand.
func agentReviewed(e Event) bool {
	return e.Kind == "reviewed" && e.Persona == "reviewer" && e.Adapter != ""
}

// leadDismissal reports whether e is a lead's dismissal of a finding: a
// disputed finding_response whose note starts "dismissed:", written by a
// session that is not a worker session of the task.
func leadDismissal(e Event, workers map[string]bool) bool {
	return e.Kind == "finding_response" && e.Verdict == "disputed" && e.Session != "" &&
		!workers[e.Session] && strings.HasPrefix(strings.TrimSpace(e.Note), "dismissed:")
}

// blockingFinding reports whether a finding blocks the unit: blocker or major.
func blockingFinding(e Event) bool {
	return e.Severity == "blocker" || e.Severity == "major"
}

// OpenFindings returns the task's review_finding events that are open, in
// ledger order (issue #389). A finding is closed only by the framework's own
// evidence: a later review round by the agent that completed without
// re-reporting it (same file and claim; a re-report is open under its new
// id), or a lead's dismissal (see leadDismissal), which also closes any later
// re-report of the same file and claim. A worker's fixed never closes a
// finding by itself: the next review round decides.
func OpenFindings(events []Event, task string) []Event {
	workers := workerSessions(events, task)
	dismissedID, dismissedKey := map[string]bool{}, map[string]bool{}
	var raised []Event
	for _, e := range events {
		if e.Task == task && e.Kind == "review_finding" {
			raised = append(raised, e)
		}
	}
	for _, e := range events {
		if e.Task == task && leadDismissal(e, workers) {
			dismissedID[e.Finding] = true
			for _, f := range raised {
				if f.Finding == e.Finding {
					dismissedKey[findingKey(f)] = true
				}
			}
		}
	}
	// open holds the findings of the last completed agent round; pending
	// those raised since it. A completed round replaces open with its own.
	var open, pending []Event
	for _, e := range events {
		if e.Task != task {
			continue
		}
		switch {
		case e.Kind == "review_finding":
			pending = append(pending, e)
		case agentReviewed(e):
			open, pending = pending, nil
		}
	}
	var out []Event
	for _, e := range append(open, pending...) {
		if !dismissedID[e.Finding] && !dismissedKey[findingKey(e)] {
			out = append(out, e)
		}
	}
	return out
}

// DismissFinding records a lead's dismissal of a review finding of task
// (issue #389): a disputed finding_response noted "dismissed: <note>". The
// session is required and refused (T4) when it is a worker session of the
// task; the finding must be one the task's reviewer raised.
func DismissFinding(dir, task, id, session, note string) error {
	if session == "" {
		return &RuleRefusal{Rule: "T4", Fix: "a lead --session is required to dismiss a finding"}
	}
	if strings.TrimSpace(note) == "" {
		return fmt.Errorf("a dismissal must say why: pass --note")
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	if r := sessionClash(task, events, session); r != "" {
		return &RuleRefusal{Rule: "T4", Fix: r}
	}
	known := false
	for _, e := range events {
		known = known || e.Task == task && e.Kind == "review_finding" && e.Finding == id
	}
	if !known {
		return fmt.Errorf("task %q has no review finding %q", task, id)
	}
	if err := AppendEvent(dir, Event{Task: task, Kind: "finding_response", Session: session, Finding: id,
		Verdict: "disputed", Note: "dismissed: " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(note), "dismissed:"))}); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	return nil
}

// findingsContract ends every findings delta: the answer the framework parses.
const findingsContract = "Fix each finding, change nothing unrelated, re-run every gate, and end your report with one line per finding: `FINDING <id>: fixed <evidence>` or `FINDING <id>: disputed <reason>`.\n"

// FindingsDelta writes .flywheel/briefs/<task>.review-<round>.txt (round is
// the task's latest agent review round): the effective brief's owns, needs
// and gate lines, then one block per open blocking finding and the answer
// contract (issue #389). It returns the repo-relative path and the number of
// findings in it; with none it writes no file and returns "", 0.
func FindingsDelta(dir string, events []Event, task string) (string, int, error) {
	var b strings.Builder
	b.WriteString("# TASK: fix the review findings\n\n")
	n := 0
	for _, f := range OpenFindings(events, task) {
		if !blockingFinding(f) {
			continue
		}
		n++
		fmt.Fprintf(&b, "FINDING %s [%s] %s:%d — %s\n", f.Finding, f.Severity, f.Path, f.LineNo, f.Title)
		fmt.Fprintf(&b, "scenario: %s\n", f.Observed)
		fmt.Fprintf(&b, "fix hint: %s\n\n", f.Ask)
	}
	if n == 0 {
		return "", 0, nil
	}
	b.WriteString(findingsContract)
	round := nextReviewRound(events, task) - 1
	path, err := writeResumeDelta(dir, task, fmt.Sprintf("%s.review-%d.txt", task, round), b.String())
	if err != nil {
		return "", 0, err
	}
	return path, n, nil
}

// findingLine is one answer line of a worker's report: `FINDING <id>: fixed
// <evidence>` or `... disputed <reason>`, tolerant of a leading - or * and of
// backticks around the line.
var findingLine = regexp.MustCompile("(?i)^[\\s>*`-]*FINDING\\s+([^\\s:`]+)\\s*:\\s*(fixed|disputed)\\b[\\s:—-]*(.*?)[\\s`]*$")

// ParseFindingResponses reads the answers to ids from a worker's report
// (issue #389): one finding_response event (Finding, Verdict fixed or
// disputed, Note) per answered id, the last line winning; answers to other
// ids are ignored. missing lists, in order, the ids with no answer line.
func ParseFindingResponses(report string, ids []string) (map[string]Event, []string) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	resp := map[string]Event{}
	for _, line := range strings.Split(strings.ReplaceAll(report, "\r\n", "\n"), "\n") {
		m := findingLine.FindStringSubmatch(line)
		if m == nil || !want[m[1]] {
			continue
		}
		resp[m[1]] = Event{Kind: "finding_response", Finding: m[1], Verdict: strings.ToLower(m[2]), Note: strings.TrimSpace(m[3])}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := resp[id]; !ok {
			missing = append(missing, id)
		}
	}
	return resp, missing
}

// ReviewLoopOptions configures ReviewLoop. Review and Correct are the loop's
// two actions, injected: the command passes ReviewAgent and a resumed
// RunResumingLimits call; tests pass fakes.
type ReviewLoopOptions struct {
	Rounds        int    // review rounds at most; <= 0 means 3
	ReviewSession string // the reviewer session
	Worker        string // the worker that corrects
	ReviewWorker  string // the worker that reviews
	Progress      io.Writer
	Review        func(round int) (ReviewAgentResult, error)
	Correct       func(deltaPath string) (Result, error)
}

// ReviewLoopResult is how the loop ended: Verdict pass when no blocking
// finding is open, else open with Open listing them.
type ReviewLoopResult struct {
	Reviews     int
	Corrections int
	Verdict     string
	Open        []Event
	Missing     []string // finding ids a correction left unanswered, every round
}

// ReviewLoop closes the review loop of a unit (issue #389), deterministically:
// review; when no blocking finding is open (OpenFindings) the verdict is
// pass; else write the FindingsDelta, run the correction on it, read the
// attempt's report and record one finding_response per open blocking finding
// — the worker's answer under the worker's session, or for an unanswered id a
// disputed response noted "missing: the worker gave no answer" — and review
// again. The agents never decide a finding is closed: only a later review
// round or a lead's dismissal does. After Rounds reviews the open blocking
// findings are returned with verdict open.
func ReviewLoop(dir, task string, o ReviewLoopOptions) (ReviewLoopResult, error) {
	if o.Review == nil || o.Correct == nil {
		return ReviewLoopResult{}, fmt.Errorf("review loop: the review and correction actions are required")
	}
	rounds := o.Rounds
	if rounds <= 0 {
		rounds = 3
	}
	var res ReviewLoopResult
	for {
		events, err := ReadEvents(dir)
		if err != nil {
			return res, err
		}
		if _, err := o.Review(nextReviewRound(events, task)); err != nil {
			return res, err
		}
		res.Reviews++
		if events, err = ReadEvents(dir); err != nil {
			return res, err
		}
		res.Open = nil
		for _, f := range OpenFindings(events, task) {
			if blockingFinding(f) {
				res.Open = append(res.Open, f)
			}
		}
		if len(res.Open) == 0 {
			res.Verdict = "pass"
			progress(o.Progress, fmt.Sprintf("%s review loop: pass after %d review(s), %d correction(s)", task, res.Reviews, res.Corrections))
			return res, nil
		}
		res.Verdict = "open"
		if res.Reviews >= rounds {
			progress(o.Progress, fmt.Sprintf("%s review loop: %d blocking finding(s) still open after %d review(s)", task, len(res.Open), res.Reviews))
			return res, nil
		}
		delta, n, err := FindingsDelta(dir, events, task)
		if err != nil {
			return res, err
		}
		progress(o.Progress, fmt.Sprintf("%s review loop: %d blocking finding(s) to the worker (%s)", task, n, delta))
		run, err := o.Correct(delta)
		if err != nil {
			return res, err
		}
		res.Corrections++
		if err := recordFindingResponses(dir, task, run, res.Open, &res); err != nil {
			return res, err
		}
	}
}

// recordFindingResponses reads the correction's report and records one
// finding_response per open finding: the worker's answer, or a missing one.
func recordFindingResponses(dir, task string, run Result, open []Event, res *ReviewLoopResult) error {
	report := ""
	if run.Attempt != "" {
		data, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", task+"."+run.Attempt+".report.md"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		report = string(data)
	}
	ids := make([]string, len(open))
	for i, f := range open {
		ids[i] = f.Finding
	}
	resp, missing := ParseFindingResponses(report, ids)
	isMissing := map[string]bool{}
	for _, id := range missing {
		isMissing[id] = true
	}
	evs := make([]Event, 0, len(ids))
	for _, id := range ids {
		e := resp[id]
		if isMissing[id] {
			e = Event{Kind: "finding_response", Finding: id, Verdict: "disputed", Note: "missing: the worker gave no answer"}
		}
		e.Task, e.Attempt, e.Session = task, run.Attempt, run.Session
		evs = append(evs, e)
	}
	res.Missing = append(res.Missing, missing...)
	if err := AppendEvents(dir, evs); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	return nil
}
