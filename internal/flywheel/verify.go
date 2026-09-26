package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VerifyOptions configures one verify pass.
type VerifyOptions struct {
	Dir   string
	Tasks []string
	All   bool
	// Workdir is the repository to resolve tree objects in for T3 (issue
	// #244): the external clone a unit's readings were measured in. When
	// empty, a workdir recorded on the task's reading events is used when it
	// still exists; otherwise the flywheel root, exactly as before.
	Workdir string
}

// VerifyItem is one rule check result for one task.
type VerifyItem struct {
	Task string `json:"task"`
	Rule string `json:"rule"`
	Pass bool   `json:"pass"`
	// Inconclusive marks a check that could not be established, distinct
	// from a violation: the pass's tree cannot be resolved in any repository
	// this verifier can see, so T3 can neither confirm the readings nor
	// assert a breach (issue #244). Pass is false; a consumer reads this
	// field to tell "broke a rule" (exit 6) from "could not check" (exit 8).
	// Omitted when false, so the --json output of an ordinary pass or
	// violation is unchanged.
	Inconclusive bool   `json:"inconclusive,omitempty"`
	Reason       string `json:"reason"`
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
		items, err := verifyTask(o.Dir, task, o.Workdir, events)
		if err != nil {
			return VerifyResult{}, err
		}
		for _, item := range items {
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
// workdir is the explicit --workdir for T3's tree resolution; when empty, each
// inspected pass resolves its own repository from that pass's recorded
// workdirs (issue #244). An operational git failure — the resolved workdir is
// not a readable repository — is an error, never a verdict.
func verifyTask(dir, task, workdir string, events []Event) ([]VerifyItem, error) {
	var items []VerifyItem
	items = append(items, ruleT1(dir, task, events)...)
	t3, err := ruleT3(dir, task, workdir, events)
	if err != nil {
		return nil, err
	}
	items = append(items, t3...)
	items = append(items, ruleT4(task, events)...)
	items = append(items, ruleT5(task, events)...)
	items = append(items, ruleT8(task, events)...)
	items = append(items, ruleR1(task, events)...)
	items = append(items, ruleW1(task, events)...)
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	items = append(items, ruleP1(task, events, cfg.PanelDimensions(), cfg.ReviewRequired())...)
	return items, nil
}

// ruleP1 checks that no inspected pass was recorded on a tree the review
// panel had not completed, where the panel applied (issue #420): review.required,
// or the panel had reviewed the task before that pass. The panel and
// review.required are today's configuration; a complete panel is every
// dimension pass in VerdictMatrix over the events before the pass, on the
// pass's tree.
func ruleP1(task string, events []Event, panel []string, required bool) []VerifyItem {
	var items []VerifyItem
	for i, e := range events {
		if e.Task != task || e.Kind != "inspected" || e.Verdict != "pass" || !panelApplies(events[:i], task, required) {
			continue
		}
		if bad := panelIncomplete(VerdictMatrix(events[:i], task, e.Tree, panel), panel); len(bad) > 0 {
			items = append(items, VerifyItem{Task: task, Rule: "P1", Pass: false,
				Reason: fmt.Sprintf("inspected pass by %q on tree %s recorded without a complete review panel: %s", e.Session, e.Tree, strings.Join(bad, ", "))})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "P1", Pass: true, Reason: "no inspected pass without a complete review panel"}}
	}
	return items
}

// ruleR1 checks that no inspected pass was recorded while a blocking review
// finding was open (issue #389): OpenFindings over the events before each
// inspected pass must hold no blocker or major.
func ruleR1(task string, events []Event) []VerifyItem {
	var items []VerifyItem
	for i, e := range events {
		if e.Task != task || e.Kind != "inspected" || e.Verdict != "pass" {
			continue
		}
		if ids := openBlockingIDs(events[:i], task); len(ids) > 0 {
			items = append(items, VerifyItem{Task: task, Rule: "R1", Pass: false,
				Reason: fmt.Sprintf("inspected pass by %q recorded while blocking review findings were open: %s", e.Session, strings.Join(ids, ", "))})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "R1", Pass: true, Reason: "no inspected pass while a blocking review finding was open"}}
	}
	return items
}

// ruleW1 checks that no withdrawn event (issue #479) took back a unit whose
// current attempt was still dispatched or running: the status the task's
// events derive to just before it (derivation order) must be anything else.
func ruleW1(task string, events []Event) []VerifyItem {
	var mine []Event
	for _, e := range events {
		if e.Task == task {
			mine = append(mine, e)
		}
	}
	evs := derivationOrder(mine)
	var items []VerifyItem
	for i, e := range evs {
		if e.Kind != "withdrawn" {
			continue
		}
		var before TaskState
		if st := Derive(evs[:i]); len(st.Tasks) > 0 {
			before = st.Tasks[0]
		}
		if before.Status == "dispatched" || before.Status == "running" {
			items = append(items, VerifyItem{Task: task, Rule: "W1", Pass: false,
				Reason: fmt.Sprintf("withdrawn event at %s while attempt %s was %s; stop the run (or wait for it to finish) before withdrawing", e.TS, before.Attempt, before.Status)})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "W1", Pass: true, Reason: "no withdrawn event while an attempt was dispatched or running"}}
	}
	return items
}

// passWorkdir resolves the repository to check one inspected pass's tree in
// (issue #244): the pass's own reading events (validated, owns_checked,
// inspected) up to and including it supply the candidates, preferring a path
// whose repository actually contains the inspected tree, then the first
// recorded path that still exists on disk, then the flywheel root. An early
// rework inspection's workdir must never pin a later pass to the wrong object
// database: each pass resolves against its own readings.
func passWorkdir(dir, task string, events []Event, i int, tree string) string {
	var recorded []string
	seen := map[string]bool{}
	for j := 0; j <= i; j++ {
		e := events[j]
		if e.Task != task || e.Workdir == "" {
			continue
		}
		switch e.Kind {
		case "validated", "owns_checked", "inspected":
			if !seen[e.Workdir] {
				seen[e.Workdir] = true
				recorded = append(recorded, e.Workdir)
			}
		}
	}
	for _, w := range recorded {
		if exists, err := treeExists(w, tree); err == nil && exists {
			return w
		}
	}
	for _, w := range recorded {
		if info, err := os.Stat(w); err == nil && info.IsDir() {
			return w
		}
	}
	return dir
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
	var items, acks []VerifyItem
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
			if note, ok := deltaAcknowledged(events, task, e); ok {
				acks = append(acks, VerifyItem{Task: task, Rule: "T1", Pass: true,
					Reason: fmt.Sprintf("acknowledged: delta for %s not retained (%s)", e.Attempt, note)})
				continue
			}
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
				Reason: fmt.Sprintf("read delta %s: %v", e.Brief, err)})
			continue
		}
		cur := sha256.Sum256(b)
		curHex := hex.EncodeToString(cur[:])
		normHex := contentSHA(b)
		if e.SHA256 != curHex && e.SHA256 != normHex {
			if note, ok := deltaAcknowledged(events, task, e); ok {
				acks = append(acks, VerifyItem{Task: task, Rule: "T1", Pass: true,
					Reason: fmt.Sprintf("acknowledged: delta for %s not retained (%s)", e.Attempt, note)})
				continue
			}
			items = append(items, VerifyItem{Task: task, Rule: "T1", Pass: false,
				Reason: fmt.Sprintf("dispatched %s sha256 %s does not match delta %s", e.Attempt, short(e.SHA256), short(curHex))})
		}
	}
	if len(items) == 0 {
		if len(acks) > 0 {
			return acks
		}
		return []VerifyItem{{Task: task, Rule: "T1", Pass: true, Reason: "every dispatched event matches its brief"}}
	}
	return append(items, acks...)
}

// deltaAcknowledged reports whether correction d's lost delta was
// acknowledged (issue #452): an amended event for task naming d's attempt,
// carrying a note, recorded after d's dispatch. It returns the latest such
// note. A correction with no acknowledgement is never waived.
func deltaAcknowledged(events []Event, task string, d Event) (string, bool) {
	t0, err := time.Parse(time.RFC3339Nano, d.TS)
	if err != nil {
		return "", false
	}
	note, ok := "", false
	for _, e := range events {
		if e.Task != task || e.Kind != "amended" || e.Attempt != d.Attempt || e.Note == "" {
			continue
		}
		if t, perr := time.Parse(time.RFC3339Nano, e.TS); perr == nil && t.After(t0) {
			note, ok = e.Note, true
		}
	}
	return note, ok
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
// An acknowledgement of a lost correction delta (an amended event naming an
// attempt, issue #452) amends no brief and is not counted.
func amendedBetween(events []Event, task, ts string) bool {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Task != task || e.Kind != "amended" || e.Attempt != "" {
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
// inspection. A pass may instead rely on readings on another tree T whose diff
// from the pass's tree lies entirely outside the unit's owns (issue #218),
// sharing requireReadings' predicate so verify accepts exactly what inspect
// does. Each inspection uses its own window, so a later correction attempt
// does not invalidate an earlier legitimate pass. A pass is measured against
// the brief header in force when it was recorded (issue #252): the header is
// resolved positionally from the events recorded up to and including the pass
// (the log is append-only, so slice order is causal order), so a correction
// delta that adds gates does not retroactively fail passes granted under the
// earlier gate set. When the pass's tree cannot be resolved in wd (an external
// --workdir this repo never had, say), a complete reading still passes on the
// ledger's own evidence, and an incomplete one is reported inconclusive — a
// verifier that cannot see the tree must not claim a violation it has not
// established (issue #244). workdir is the explicit --workdir, or "" to resolve
// each inspected pass's repository from that pass's own recorded workdirs
// (issue #244). An operational git failure while examining the tree is an
// error naming the path, never a verdict.
func ruleT3(dir, task, workdir string, events []Event) ([]VerifyItem, error) {
	if _, err := briefHeaderFor(dir, task, events); err != nil {
		return []VerifyItem{{Task: task, Rule: "T3", Pass: false, Reason: err.Error()}}, nil
	}
	var items []VerifyItem
	// An external reading (issue #367) must name its evidence, session and
	// commit; one that does not is a violation on its own, and never counts
	// towards a pass (externalReadingOK in the reading predicates).
	for _, e := range events {
		if e.Task == task && e.Source != "" && (e.Kind == "validated" || e.Kind == "owns_checked") && !externalReadingOK(e) {
			items = append(items, VerifyItem{Task: task, Rule: "T3", Pass: false,
				Reason: fmt.Sprintf("external %s reading on tree %s lacks evidence, a session or the commit it measured", e.Kind, short(e.Tree))})
		}
	}
	var attested []string
	for i, insp := range events {
		if insp.Task != task || insp.Kind != "inspected" || insp.Verdict != "pass" {
			continue
		}
		// The header in force for this pass is what AttemptBrief saw over the
		// events recorded up to and including it: events[:i+1]. Slice position
		// is the causal order here, because the log is append-only and
		// RFC3339Nano timestamps can repeat (imported or replayed logs); a
		// timestamp filter would drag a later correction sharing the pass's TS
		// into its header. latestFinishedBefore below deliberately stays
		// timestamp-based: that is a chronological question ("what was the last
		// finished event before this moment"), where timestamps are the right
		// tool.
		header, err := briefHeaderFor(dir, task, events[:i+1])
		if err != nil {
			items = append(items, VerifyItem{Task: task, Rule: "T3", Pass: false, Reason: err.Error()})
			continue
		}
		latest := latestFinishedBefore(events, task, insp.TS)
		wd := workdir
		if wd == "" {
			wd = passWorkdir(dir, task, events, i, insp.Tree)
		}
		outcome, err := readingsOutcomeFor(absPath(wd), events, header, task, insp.Tree, latest)
		if err != nil {
			return nil, fmt.Errorf("verify T3 pass %s: %w", short(insp.Tree), err)
		}
		switch outcome {
		case readingsOK:
			if ev := attestedEvidence(events, task, insp.Tree); ev != "" {
				attested = append(attested, ev)
			}
			continue
		case readingsInconclusive:
			items = append(items, VerifyItem{Task: task, Rule: "T3", Pass: false, Inconclusive: true,
				Reason: fmt.Sprintf("inspected pass on tree %s cannot be verified: %s is not a tree in this repository; pass --workdir to the repository the readings were taken in", short(insp.Tree), short(insp.Tree))})
		case readingsViolation:
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
			if missing == "" {
				missing = "an examinable tree"
			}
			items = append(items, VerifyItem{Task: task, Rule: "T3", Pass: false,
				Reason: fmt.Sprintf("inspected pass on tree %s lacks %s after the latest finished event before it", short(insp.Tree), missing)})
		}
	}
	if len(items) == 0 {
		reason := "every inspected pass has readings"
		if len(attested) > 0 {
			reason += "; attested: " + strings.Join(attested, ", ")
		}
		return []VerifyItem{{Task: task, Rule: "T3", Pass: true, Reason: reason}}, nil
	}
	return items, nil
}

// attestedEvidence returns the evidence of the latest sound external validated
// reading of task on tree (issue #367), or "" when tree's readings were all
// measured by flywheel.
func attestedEvidence(events []Event, task, tree string) string {
	ev := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "validated" && e.Tree == tree && e.Source == "external" && externalReadingOK(e) {
			ev = e.Evidence
		}
	}
	return ev
}

// readingsOutcome is how a T3 readings check for one inspected pass ended.
type readingsOutcome int

const (
	// readingsOK: every gate and owns_checked reading exists on the ledger's
	// own evidence, or the issue #218 relaxation qualifies another tree.
	readingsOK readingsOutcome = iota
	// readingsViolation: the tree resolves in this repository and no reading
	// qualifies; the pass broke T3.
	readingsViolation
	// readingsInconclusive: the tree cannot be resolved in any repository this
	// verifier can see, so the relaxation cannot be evaluated and a violation
	// is not established (issue #244).
	readingsInconclusive
)

// readingsOutcomeFor classifies one pass's T3 check. An empty tree cannot name
// an object in any repository: it is a malformed record, so it is classified a
// violation before the strict readings check and before any repository lookup —
// an inconclusive result reported as a failure is a bug, and a violation
// quietly downgraded to inconclusive is worse. The strict check runs next and
// needs no git: a complete reading on the ledger's own evidence is readingsOK
// even when the tree is gone (issue #244). Only when the strict check fails and
// the tree cannot be resolved is the result readingsInconclusive; an
// unresolvable tree must never be reported as a violation, and a violation
// must never be downgraded to inconclusive when the tree is there to check. An
// operational git failure — an invalid workdir or an unreadable repository — is
// an error, not a verdict (issue #244).
func readingsOutcomeFor(wd string, events []Event, header BriefHeader, task, tree string, after time.Time) (readingsOutcome, error) {
	if tree == "" {
		return readingsViolation, nil
	}
	if allReadings(events, header, task, tree, after) {
		return readingsOK, nil
	}
	exists, err := treeExists(wd, tree)
	if err != nil {
		return readingsViolation, err
	}
	if !exists {
		return readingsInconclusive, nil
	}
	_, ok, err := relaxedReadingTree(wd, events, header, task, tree, after)
	if err != nil {
		return readingsViolation, err
	}
	if ok {
		return readingsOK, nil
	}
	return readingsViolation, nil
}

// latestFinishedBefore returns the latest finished event's timestamp for task
// that is strictly before ts, or the zero time when there is none. This stays
// timestamp-based on purpose, unlike ruleT3's header resolution: it answers a
// chronological question ("what was the last finished event before this
// moment"), while which events had been recorded by a pass is a positional
// question answered by the slice prefix.
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

// workerSessions returns the set of sessions that wrote the task's started,
// finished, dispatched, report or worker_plan events.
func workerSessions(events []Event, task string) map[string]bool {
	workers := map[string]bool{}
	for _, e := range events {
		if e.Task == task && e.Session != "" {
			switch e.Kind {
			case "started", "finished", "dispatched", "report", "worker_plan":
				workers[e.Session] = true
			}
		}
	}
	return workers
}

// ruleT4 checks that no inspected, excepted or external (attested, issue #367)
// event comes from a worker session.
func ruleT4(task string, events []Event) []VerifyItem {
	workers := workerSessions(events, task)
	var items []VerifyItem
	for _, e := range events {
		if e.Task != task || e.Session == "" {
			continue
		}
		if e.Kind == "inspected" && workers[e.Session] {
			items = append(items, VerifyItem{Task: task, Rule: "T4", Pass: false,
				Reason: fmt.Sprintf("inspected event uses worker session %q", e.Session)})
		}
		if e.Kind == "excepted" && workers[e.Session] {
			items = append(items, VerifyItem{Task: task, Rule: "T4", Pass: false,
				Reason: fmt.Sprintf("excepted event uses worker session %q", e.Session)})
		}
		if e.Source == "external" && workers[e.Session] {
			items = append(items, VerifyItem{Task: task, Rule: "T4", Pass: false,
				Reason: fmt.Sprintf("external %s reading attested by worker session %q", e.Kind, e.Session)})
		}
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T4", Pass: true, Reason: "no inspected or excepted event from a worker session"}}
	}
	return items
}

// ruleT5 checks that every landed event has an earlier inspected pass or a
// recorded exception.
func ruleT5(task string, events []Event) []VerifyItem {
	workers := workerSessions(events, task)
	var items []VerifyItem
	for _, e := range events {
		if e.Task != task || e.Kind != "landed" {
			continue
		}
		if earlierInspectedPass(events, task, e.TS) {
			continue
		}
		if ex := earlierException(events, task, e.Commit, e.TS, workers); ex != nil {
			items = append(items, VerifyItem{Task: task, Rule: "T5", Pass: true,
				Reason: fmt.Sprintf("landed on a recorded exception by %s: %s", ex.Session, ex.Note)})
			continue
		}
		items = append(items, VerifyItem{Task: task, Rule: "T5", Pass: false,
			Reason: "landed event has no earlier inspected pass"})
	}
	if len(items) == 0 {
		return []VerifyItem{{Task: task, Rule: "T5", Pass: true, Reason: "no landed event without a prior inspected pass"}}
	}
	return items
}

// earlierException returns the task's valid excepted event at or before ts
// that covers commit — the exception must name the landed commit, so evidence
// for one commit never authorises another — with a non-empty note and a
// non-worker session, or nil when there is none.
func earlierException(events []Event, task, commit, ts string, workers map[string]bool) *Event {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil
	}
	for _, e := range events {
		if e.Task != task || e.Kind != "excepted" || e.Note == "" || e.Session == "" || commit == "" || e.Commit != commit {
			continue
		}
		if workers[e.Session] {
			continue
		}
		if t, perr := time.Parse(time.RFC3339Nano, e.TS); perr == nil && !t.After(t0) {
			return &e
		}
	}
	return nil
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
