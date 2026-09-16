package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// VerifyOptions configures one verify pass.
type VerifyOptions struct {
	Dir   string
	Tasks []string
	All   bool
}

// VerifyItem is one rule check result for one task.
type VerifyItem struct {
	Task   string `json:"task"`
	Rule   string `json:"rule"`
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

// VerifyResult is the machine-readable output of a verify pass.
type VerifyResult struct {
	Passed bool         `json:"passed"`
	Items  []VerifyItem `json:"items"`
}

// VerifyTasks checks every requested task against the poka-yoke rules T1, T3,
// T4, T5 and T8 and reports one item per check.
func VerifyTasks(dir string, o VerifyOptions) (VerifyResult, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return VerifyResult{}, err
	}
	tasks := o.Tasks
	if o.All {
		tasks = allTasks(events)
	}
	if len(tasks) == 0 {
		if o.All {
			// An empty but valid log verifies clean; explicitly named tasks
			// still run the rules below.
			return VerifyResult{Passed: true}, nil
		}
		return VerifyResult{}, fmt.Errorf("verify: no tasks to check; pass task ids or --all")
	}
	res := VerifyResult{Passed: true}
	for _, task := range tasks {
		for _, item := range verifyTask(o.Dir, task, events) {
			if !item.Pass {
				res.Passed = false
			}
			res.Items = append(res.Items, item)
		}
	}
	return res, nil
}

// allTasks returns the distinct task ids present in the event log.
func allTasks(events []Event) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if e.Task != "" && !seen[e.Task] {
			seen[e.Task] = true
			out = append(out, e.Task)
		}
	}
	return out
}

// verifyTask runs every rule for one task and returns the per-rule items.
func verifyTask(dir, task string, events []Event) []VerifyItem {
	var items []VerifyItem
	items = append(items, ruleT1(dir, task, events)...)
	items = append(items, ruleT3(dir, task, events)...)
	items = append(items, ruleT4(task, events)...)
	items = append(items, ruleT5(task, events)...)
	items = append(items, ruleT8(task, events)...)
	return items
}

// ruleT1 checks that each dispatched event's sha256 matches the prompt it was
// dispatched with. A fresh attempt (r*) must match the planned brief's SHA-256
// at that time, unless an amended event came in between (compare with the
// current brief when it is the last change). A correction attempt (c*) must
// match the delta file its dispatched.Brief names: a missing path or file and
// a tampered delta all fail naming the delta, and amendments never waive a
// correction's hash check.
func ruleT1(dir, task string, events []Event) []VerifyItem {
	planned := plannedBriefOnly(events, task)
	var items []VerifyItem
	if planned != "" {
		briefPath := planned
		if !filepath.IsAbs(briefPath) {
			briefPath = filepath.Join(dir, briefPath)
		}
		b, err := os.ReadFile(briefPath)
		if err != nil {
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false, Reason: fmt.Sprintf("read brief: %v", err)})
		} else {
			cur := sha256.Sum256(b)
			curHex := hex.EncodeToString(cur[:])
			normHex := contentSHA(b)
			for _, e := range events {
				if e.Task != task || e.Kind != "dispatched" || e.SHA256 == "" || isCorrection(e.Attempt) {
					continue
				}
				if amendedBetween(events, task, e.TS) {
					continue // an amendment explains the change; compare when it is the last change
				}
				if e.SHA256 != curHex && e.SHA256 != normHex {
					items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
						Reason: fmt.Sprintf("dispatched sha256 %s does not match current brief %s", short(e.SHA256), short(curHex))})
				}
			}
		}
	}
	// Corrections hash their own delta: the path recorded at dispatch, read
	// back and compared now. Amendments to the planned brief never explain
	// away a tampered or missing delta.
	for _, e := range events {
		if e.Task != task || e.Kind != "dispatched" || e.SHA256 == "" || !isCorrection(e.Attempt) {
			continue
		}
		if e.Brief == "" {
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
				Reason: fmt.Sprintf("dispatched %s records no delta path in brief", e.Attempt)})
			continue
		}
		delta := e.Brief
		if !filepath.IsAbs(delta) {
			delta = filepath.Join(dir, delta)
		}
		b, err := os.ReadFile(delta)
		if err != nil {
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
				Reason: fmt.Sprintf("read delta %s: %v", e.Brief, err)})
			continue
		}
		cur := sha256.Sum256(b)
		curHex := hex.EncodeToString(cur[:])
		normHex := contentSHA(b)
		if e.SHA256 != curHex && e.SHA256 != normHex {
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
				Reason: fmt.Sprintf("dispatched %s sha256 %s does not match delta %s", e.Attempt, short(e.SHA256), short(curHex))})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T1", Pass: true, Reason: "every dispatched event matches its brief"}}
	}
	return items
}

// plannedBriefOnly returns task's latest `planned` event's brief path,
// deliberately ignoring `amended` events: T1 checks a dispatched hash
// against the brief as it was planned, unlike AttemptBrief's amendment-aware
// base (verify's T1 rule is unchanged by that fix).
func plannedBriefOnly(events []Event, task string) string {
	planned := ""
	for _, e := range events {
		if e.Task != task || e.Kind != "planned" || e.Brief == "" {
			continue
		}
		planned = e.Brief
	}
	return planned
}

// isCorrection reports whether an attempt id is a correction attempt (c*).
func isCorrection(attempt string) bool {
	return len(attempt) >= 2 && attempt[0] == 'c'
}

// amendedBetween reports whether an amended event for task occurred after ts.
func amendedBetween(events []Event, task, ts string) bool {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Task != task || e.Kind != "amended" {
			continue
		}
		if t, perr := time.Parse(time.RFC3339Nano, e.TS); perr == nil && t.After(t0) {
			return true
		}
	}
	return false
}

// ruleT3 checks that every inspected pass has passing supervisor validated
// events for all declared gates on the same tree, and a clean owns_checked for
// that tree, recorded after the latest finished event that precedes that
// inspection. Each inspection uses its own window, so a later correction
// attempt does not invalidate an earlier legitimate pass.
func ruleT3(dir, task string, events []Event) []VerifyItem {
	header, err := briefHeaderFor(dir, task, events)
	if err != nil {
		return []VerifyItem{{Task: task, Rule: "T3", Pass: false, Reason: err.Error()}}
	}
	var items []VerifyItem
	for _, insp := range events {
		if insp.Task != task || insp.Kind != "inspected" || insp.Verdict != "pass" {
			continue
		}
		latest := latestFinishedBefore(events, task, insp.TS)
		missing := ""
		for i := range header.Gates {
			idx := fmt.Sprintf("%d", i+1)
			if !hasPassingValidated(events, task, idx, insp.Tree, latest) {
				missing = fmt.Sprintf("gate %s", idx)
				break
			}
		}
		if missing == "" && !hasCleanOwnsChecked(events, task, insp.Tree, latest) {
			missing = "clean owns_checked"
		}
		if missing != "" {
			items = append(items, VerifyItem{Task: task, Rule: "T3", Pass: false,
				Reason: fmt.Sprintf("inspected pass on tree %s lacks %s after the latest finished event before it", short(insp.Tree), missing)})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T3", Pass: true, Reason: "every inspected pass has readings"}}
	}
	return items
}

// latestFinishedBefore returns the latest finished event's timestamp for task
// that is strictly before ts, or the zero time when there is none.
func latestFinishedBefore(events []Event, task, ts string) time.Time {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range events {
		if e.Task != task || e.Kind != "finished" {
			continue
		}
		if t, cerr := time.Parse(time.RFC3339Nano, e.TS); cerr == nil && t.Before(t0) && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// briefHeaderFor loads the header to measure for the task's current attempt
// (base brief plus any correction delta), or an error naming the problem.
func briefHeaderFor(dir, task string, events []Event) (BriefHeader, error) {
	header, _, err := AttemptBrief(dir, events, task)
	return header, err
}

// ruleT4 checks that no inspected event comes from a worker session.
func ruleT4(task string, events []Event) []VerifyItem {
	workers := map[string]bool{}
	for _, e := range events {
		if e.Task == task && e.Session != "" {
			switch e.Kind {
			case "started", "finished", "dispatched", "report", "worker_plan":
				workers[e.Session] = true
			}
		}
	}
	var items []VerifyItem
	for _, e := range events {
		if e.Task != task || e.Kind != "inspected" || e.Session == "" {
			continue
		}
		if workers[e.Session] {
			items = append(items, VerifyItem{Task: task, Rule: "T4", Pass: false,
				Reason: fmt.Sprintf("inspected event uses worker session %q", e.Session)})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T4", Pass: true, Reason: "no inspected event from a worker session"}}
	}
	return items
}

// ruleT5 checks that every landed event has an earlier inspected pass.
func ruleT5(task string, events []Event) []VerifyItem {
	var items []VerifyItem
	for _, e := range events {
		if e.Task != task || e.Kind != "landed" {
			continue
		}
		if !earlierInspectedPass(events, task, e.TS) {
			items = append(items, VerifyItem{Task: task, Rule: "T5", Pass: false,
				Reason: "landed event has no earlier inspected pass"})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T5", Pass: true, Reason: "no landed event without a prior inspected pass"}}
	}
	return items
}

// earlierInspectedPass reports whether task has an inspected pass before ts.
func earlierInspectedPass(events []Event, task, ts string) bool {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Task != task || e.Kind != "inspected" || e.Verdict != "pass" {
			continue
		}
		if t, perr := time.Parse(time.RFC3339Nano, e.TS); perr == nil && t.Before(t0) {
			return true
		}
	}
	return false
}

// ruleT8 checks personas: validated and owns_checked carry supervisor;
// inspected carries inspector or lead.
func ruleT8(task string, events []Event) []VerifyItem {
	var items []VerifyItem
	for _, e := range events {
		if e.Task != task {
			continue
		}
		switch e.Kind {
		case "validated", "owns_checked":
			if e.Persona != "supervisor" {
				items = append(items, VerifyItem{Task: task, Rule: "T8", Pass: false,
					Reason: fmt.Sprintf("%s event carries persona %q, want supervisor", e.Kind, e.Persona)})
			}
		case "inspected":
			if e.Persona != "inspector" && e.Persona != "lead" {
				items = append(items, VerifyItem{Task: task, Rule: "T8", Pass: false,
					Reason: fmt.Sprintf("inspected event carries persona %q, want inspector or lead", e.Persona)})
			}
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T8", Pass: true, Reason: "personas are correct"}}
	}
	return items
}

// short truncates a long hex hash for a readable reason.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
