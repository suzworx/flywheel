package flywheel

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
	var tree string
	note := o.Note
	if o.Verdict == "pass" {
		res := requireReadings(o.Dir, wd(o.Workdir, o.Dir), task, events)
		if res.err != nil {
			return res.err
		}
		if res.refusal.Rule != "" {
			return &res.refusal
		}
		// Record exactly the tree the T3 check measured, never a second hash
		// taken later: the worktree could have changed in between, and the
		// ledger must not bless a tree no gate ever measured (issue #241).
		tree = res.currentTree
		if res.readingTree != "" {
			suffix := fmt.Sprintf("reading from tree %s (diff outside owns)", res.readingTree)
			if note != "" {
				note = note + "; " + suffix
			} else {
				note = suffix
			}
		}
	} else {
		tree, err = hashTree(wd(o.Workdir, o.Dir))
		if err != nil {
			return err
		}
	}
	if err := AppendEvent(o.Dir, Event{
		TS: "", Task: task, Kind: "inspected", Verdict: o.Verdict,
		Tree: tree, Session: o.Session, Note: note, Persona: "inspector",
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

// readingsResult is the outcome of a T3 readings check. currentTree is the
// tree the check actually measured — the tree the inspected event must
// record. readingTree is the tree the accepted reading came from when it
// differs from currentTree (issue #218 relaxation), or "" when currentTree
// itself qualified. refusal is the T3 refusal (Rule non-empty) when the check
// failed; err is a real failure rather than a refusal.
type readingsResult struct {
	currentTree string
	readingTree string
	refusal     RuleRefusal
	err         error
}

// hashTree measures the working tree. It is a package variable so a test can
// rebind it and interleave a worktree change between the readings check and
// the inspected event (issue #241); production code never rebinds it.
var hashTree = treeHash

// requireReadings enforces T3 for a pass verdict. The path is unchanged
// except that the tree measured for the check is returned in currentTree so
// the caller records exactly that tree: a second hash taken later could name
// a tree no gate ever measured (issue #241). When the current tree has no
// complete reading, T3 is relaxed (issue #218): the reading may be taken on
// another tree whose diff from the current tree lies entirely outside the
// unit's owns. The accepted tree is returned in readingTree so the inspected
// event's note can name it. A non-empty refusal.Rule is a refusal.
func requireReadings(dir, wd, task string, events []Event) readingsResult {
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		if errors.Is(err, errNoPlannedBrief) {
			return readingsResult{refusal: RuleRefusal{Rule: "T3", Fix: "task has no planned brief"}}
		}
		return readingsResult{err: err}
	}
	if len(header.Gates) == 0 {
		return readingsResult{refusal: RuleRefusal{Rule: "T3", Fix: "brief declares no gate: lines; add gate: lines to the brief header"}}
	}
	tree, err := hashTree(wd)
	if err != nil {
		return readingsResult{err: err}
	}
	latest := latestFinished(events, task)
	t, ok, err := readingsForPass(wd, events, header, task, tree, latest)
	if err != nil {
		return readingsResult{err: err}
	}
	if ok {
		return readingsResult{currentTree: tree, readingTree: t}
	}
	return readingsResult{currentTree: tree, refusal: readingsRefusal(events, header, task, tree, latest)}
}

// readingsForPass reports the tree a pass on tree may rely on after the
// boundary: "" with ok when tree itself has a complete reading (today's rule),
// another tree T with ok when the issue #218 relaxation qualifies it, or
// ok=false when no reading qualifies. The relaxation re-checks the diff
// against the repository — the note on an inspected event is never trusted.
// A tree that no longer exists in the repository cannot be examined, so a pass
// on it never qualifies. requireReadings and ruleT3 share this predicate, so
// inspect and verify accept exactly the same set of passes.
func readingsForPass(wd string, events []Event, header BriefHeader, task, tree string, after time.Time) (string, bool, error) {
	if !treeExists(wd, tree) {
		return "", false, nil
	}
	if allReadings(events, header, task, tree, after) {
		return "", true, nil
	}
	return relaxedReadingTree(wd, events, header, task, tree, after)
}

// allReadings reports whether task has, after after, a passing validated
// reading on tree for every gate and live gate the brief header declares, and
// a clean owns_checked on the same tree.
func allReadings(events []Event, header BriefHeader, task, tree string, after time.Time) bool {
	for i := range header.Gates {
		idx := fmt.Sprintf("%d", i+1)
		if !hasPassingValidated(events, task, idx, tree, after) {
			return false
		}
	}
	for i := range header.LiveGates {
		n := i + 1
		idx := fmt.Sprintf("live%d", n)
		if !hasPassingValidated(events, task, idx, tree, after) {
			return false
		}
	}
	return hasCleanOwnsChecked(events, task, tree, after)
}

// readingsRefusal is today's T3 refusal for a pass with no complete reading on
// tree: the first gate, live-gate or owns_checked that is missing. It is
// returned unchanged when no other tree qualifies for the issue #218
// relaxation.
func readingsRefusal(events []Event, header BriefHeader, task, tree string, after time.Time) RuleRefusal {
	for i := range header.Gates {
		idx := fmt.Sprintf("%d", i+1)
		if !hasPassingValidated(events, task, idx, tree, after) {
			return RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("no passing supervisor validated reading for gate %s on tree %s after the latest finished event; run: flywheel validate %s", idx, tree, task)}
		}
	}
	for i := range header.LiveGates {
		n := i + 1
		idx := fmt.Sprintf("live%d", n)
		if !hasPassingValidated(events, task, idx, tree, after) {
			return RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("no passing supervisor validated reading for live-gate %d on tree %s after the latest finished event; run: flywheel validate %s --live", n, tree, task)}
		}
	}
	return RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("no clean owns_checked for tree %s after the latest finished event; run: flywheel validate %s", tree, task)}
}

// relaxedReadingTree implements the issue #218 relaxation: when the current
// tree has no complete reading, accept the most recent passing reading on some
// other tree T whose diff from the current tree lies entirely outside the
// unit's owns. All of a task's accepted readings must come from the same T.
func relaxedReadingTree(wd string, events []Event, header BriefHeader, task, tree string, after time.Time) (string, bool, error) {
	cands := map[string]time.Time{}
	for _, e := range events {
		if e.Task != task || e.Tree == "" || e.Tree == tree {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil || !t.After(after) {
			continue
		}
		qualifies := e.Kind == "validated" && e.Reason != "host-blocked" && e.RC != nil && *e.RC == 0 ||
			e.Kind == "owns_checked" && len(e.Outside) == 0
		if qualifies && t.After(cands[e.Tree]) {
			cands[e.Tree] = t
		}
	}
	type cand struct {
		tree string
		at   time.Time
	}
	order := make([]cand, 0, len(cands))
	for tr, at := range cands {
		order = append(order, cand{tr, at})
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].at.After(order[j].at) })
	for _, c := range order {
		if !allReadings(events, header, task, c.tree, after) {
			continue
		}
		if !treeExists(wd, c.tree) {
			continue
		}
		ok, err := diffOutsideOwns(wd, c.tree, tree, header.Owns)
		if err != nil {
			return "", false, err
		}
		if ok {
			return c.tree, true, nil
		}
	}
	return "", false, nil
}

// diffOutsideOwns reports whether every path that differs between the two tree
// ids lies outside owns. The comparison is a read-only git diff --name-only
// between two tree ids; the shared git index is never touched (issue #218).
func diffOutsideOwns(wd, from, to string, owns []string) (bool, error) {
	out, err := gitRead(wd, []string{"diff", "--name-only", from, to})
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" && ownsContains(owns, p) {
			return false, nil
		}
	}
	return true, nil
}

// treeExists reports whether the named git object exists in the repository at
// wd. A tree an old reading names may have been garbage-collected; a reading on
// a tree that cannot be examined never qualifies (issue #218).
func treeExists(wd, tree string) bool {
	_, err := gitRead(wd, []string{"cat-file", "-e", tree})
	return err == nil
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
